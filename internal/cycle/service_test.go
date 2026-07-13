package cycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

type fakeKernel struct {
	cycles       map[string]domain.Cycle
	latest       string
	lockHeld     bool
	setCalls     int
	events       []domain.Stage
	replay       []string
	advanceCalls int
}

func newFakeKernel() *fakeKernel {
	return &fakeKernel{cycles: map[string]domain.Cycle{}}
}

func (k *fakeKernel) Health(context.Context) error { return nil }
func (k *fakeKernel) AcquireCycleLock(context.Context, string, string, string) error {
	if k.lockHeld {
		return adapters.ErrLockHeld
	}
	k.lockHeld = true
	return nil
}
func (k *fakeKernel) ReleaseCycleLock(context.Context, string, string) error {
	k.lockHeld = false
	return nil
}
func (k *fakeKernel) SetCycle(_ context.Context, cycle domain.Cycle) error {
	k.setCalls++
	data, _ := json.Marshal(cycle)
	var clone domain.Cycle
	_ = json.Unmarshal(data, &clone)
	k.cycles[cycle.ID] = clone
	return nil
}
func (k *fakeKernel) GetCycle(_ context.Context, id string) (domain.Cycle, error) {
	cycle, ok := k.cycles[id]
	if !ok {
		return domain.Cycle{}, adapters.ErrNotFound
	}
	return cycle, nil
}
func (k *fakeKernel) SetLatestCycle(_ context.Context, _, id string) error {
	k.latest = id
	return nil
}
func (k *fakeKernel) CreateCycleRun(context.Context, string, string, map[string]any) (string, error) {
	return "run-1", nil
}
func (k *fakeKernel) AdvanceRun(context.Context, string) error {
	k.advanceCalls++
	return nil
}
func (k *fakeKernel) RecordReplayInput(_ context.Context, _, kind, key, _, _ string) error {
	k.replay = append(k.replay, kind+":"+key)
	return nil
}
func (k *fakeKernel) RecordStageEvent(_ context.Context, _, _ string, stage domain.Stage, _ string) error {
	k.events = append(k.events, stage)
	return nil
}
func (k *fakeKernel) Observation(context.Context, int) ([]adapters.Discovery, adapters.InterestProfile, error) {
	return []adapters.Discovery{{ID: "disc-1", Source: "interject", Title: "Roadmap refresh overhead", Status: "scored", RelevanceScore: 0.8}}, adapters.InterestProfile{KeywordWeights: `{}`, SourceWeights: `{}`}, nil
}

type fakeBacklog struct {
	items       []adapters.Bead
	createCalls int
	created     domain.Candidate
}

func (b *fakeBacklog) List(context.Context) ([]adapters.Bead, error) {
	return append([]adapters.Bead(nil), b.items...), nil
}
func (b *fakeBacklog) CreateExperiment(_ context.Context, cycleID, fingerprint string, candidate domain.Candidate) (string, error) {
	b.createCalls++
	b.created = candidate
	id := "Revel-experiment"
	b.items = append(b.items, adapters.Bead{
		ID: id, Title: candidate.Title, Status: "open", Priority: 4,
		Labels: []string{"remontoire:cycle:" + cycleID, "remontoire:fingerprint:" + fingerprint},
	})
	return id, nil
}

type fakePolicy struct {
	weights  map[string]int
	degraded bool
}

func (p fakePolicy) Weights(context.Context) (map[string]int, bool) {
	return p.weights, p.degraded
}

type dynamicJudge struct {
	call func(harness.JudgmentRequest) domain.Judgment
}

func (j dynamicJudge) Judge(_ context.Context, request harness.JudgmentRequest) (domain.Judgment, harness.Metadata, error) {
	return j.call(request), harness.Metadata{Backend: "fake", Model: "fixture"}, nil
}

func selectedJudgment(t *testing.T, request harness.JudgmentRequest, repository string) domain.Judgment {
	t.Helper()
	var observation Observation
	if err := json.Unmarshal(request.Observation, &observation); err != nil {
		t.Fatalf("decode observation: %v", err)
	}
	digest := ""
	for _, artifact := range observation.Artifacts {
		if artifact.Kind == "beads" {
			digest = artifact.Digest
		}
	}
	if digest == "" {
		t.Fatal("observation has no beads digest")
	}
	selected := 0
	return domain.Judgment{
		SchemaVersion: domain.JudgmentSchemaV1,
		SelectedIndex: &selected,
		Opportunities: []domain.Candidate{
			{
				Title: "Measure roadmap parse cost", Summary: "Retire uncertainty about a suspected refresh bottleneck.", Project: "Remontoire", Priority: 4,
				Impact: 0.7, Uncertainty: 0.9, Cost: 0.2, Risk: 0.1, PolicyFit: 0.8,
				Evidence: []domain.EvidenceRef{{Kind: "bead", ID: "Revel-source", Digest: digest}},
				Contract: domain.EvidenceContract{
					SchemaVersion: domain.ContractSchemaV1,
					Hypothesis:    "Roadmap parsing accounts for at least twenty percent of refresh time.",
					Falsifier:     "Profiling shows parsing below twenty percent or the target is missed.",
					Repository:    repository, AllowedPaths: []string{"internal/roadmap"},
					Metric:         domain.Metric{Name: "refresh_ms", Unit: "ms", Direction: domain.DirectionMinimize, Source: domain.MetricSourceWallDurationMS, Baseline: 100, Target: 80},
					Benchmark:      []string{"go", "test", "./internal/roadmap"},
					Budget:         domain.Budget{MaxDurationSeconds: 600, MaxTurns: 6, MaxCostUSD: 2},
					StopConditions: []string{"tests fail", "allowed path boundary crossed"}, Executor: "codex",
					PromotionCriteria: "target met and tests pass", ClosureCriteria: "target missed or tests fail",
				},
			},
		},
	}
}

func testService(t *testing.T, mode domain.Mode) (*Service, *fakeKernel, *fakeBacklog) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "internal", "roadmap"), 0o755); err != nil {
		t.Fatal(err)
	}
	kernel := newFakeKernel()
	backlog := &fakeBacklog{items: []adapters.Bead{{ID: "Revel-source", Title: "Source", Status: "open", Priority: 2}}}
	service := &Service{
		Config: Config{
			Portfolio: "sylveste", ProjectDir: repo, ArtifactRoot: filepath.Join(repo, ".remontoire"),
			JudgmentSchemaPath: filepath.Join(root, "judgment.json"), MaxInputBytes: 1 << 20,
			AllowedRepositoryRoots: []string{root}, DiscoveryLimit: 50, LockTimeout: "0s", DefaultMode: mode,
		},
		Kernel: kernel, Backlog: backlog,
		Policy: fakePolicy{weights: map[string]int{"Revel-source": 6}},
		Store:  FileStore{Root: filepath.Join(repo, ".remontoire")},
		Now:    func() time.Time { return time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC) },
		NewID:  func(time.Time) (string, error) { return "cycle-1", nil },
	}
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		return selectedJudgment(t, request, repo)
	}}
	return service, kernel, backlog
}

func TestProposalCycleCreatesExactlyOneP4Experiment(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Stage != domain.StageAwaitingApproval || cycle.ExperimentBeadID != "Revel-experiment" {
		t.Fatalf("cycle = %#v", cycle)
	}
	if backlog.createCalls != 1 || backlog.created.Priority != 4 {
		t.Fatalf("create calls=%d candidate=%#v", backlog.createCalls, backlog.created)
	}
	if cycle.CandidateHash == "" || cycle.ContractHash == "" || cycle.RunID != "run-1" {
		t.Fatalf("cycle hashes/run missing: %#v", cycle)
	}
	if len(kernel.replay) < 4 {
		t.Fatalf("replay inputs = %#v", kernel.replay)
	}
	foundObservation := false
	for _, artifact := range cycle.Artifacts {
		if artifact.Kind == "observation" {
			foundObservation = true
		}
		if artifact.Kind == "cycle-state" {
			t.Fatal("cycle projection must not contain a self-referential digest")
		}
	}
	if !foundObservation {
		t.Fatal("cycle does not retain the exact composite observation")
	}
	if _, err := service.ResumeProposal(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	if backlog.createCalls != 1 {
		t.Fatalf("resume created a second experiment: %d", backlog.createCalls)
	}
}

func TestProposalResumeAfterCreateBeforeStateDoesNotDuplicate(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	cycle.Stage = domain.StageRanked
	cycle.ExperimentBeadID = ""
	kernel.cycles[cycle.ID] = cycle

	resumed, err := service.ResumeProposal(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if backlog.createCalls != 1 || resumed.ExperimentBeadID != "Revel-experiment" || resumed.Stage != domain.StageAwaitingApproval {
		t.Fatalf("resumed=%#v createCalls=%d", resumed, backlog.createCalls)
	}
}

func TestDuplicateFingerprintCompletesAsNoOp(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeProposal)
	judge := service.Judge.(dynamicJudge)
	var fingerprint string
	judge.call = func(request harness.JudgmentRequest) domain.Judgment {
		j := selectedJudgment(t, request, service.Config.ProjectDir)
		fingerprint = domain.FingerprintCandidate(j.Opportunities[0])
		return j
	}
	service.Judge = judge
	backlog.items = append(backlog.items, adapters.Bead{
		ID: "Revel-existing", Status: "open", Priority: 4,
		Labels: []string{"remontoire:fingerprint:" + strings.Repeat("0", 64)},
	})
	// Compute the exact stable fingerprint independently of the observation call.
	request := harness.JudgmentRequest{Observation: mustObservationJSON(t, service, backlog.items)}
	j := selectedJudgment(t, request, service.Config.ProjectDir)
	fingerprint = domain.FingerprintCandidate(j.Opportunities[0])
	backlog.items[len(backlog.items)-1].Labels = []string{"remontoire:fingerprint:" + fingerprint}

	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Stage != domain.StageCompleted || backlog.createCalls != 0 || !strings.Contains(cycle.Judgment.NoOpReason, "duplicate") {
		t.Fatalf("cycle=%#v createCalls=%d", cycle, backlog.createCalls)
	}
}

func TestShadowCycleNeverMutatesBacklog(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeShadow)
	cycle, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Stage != domain.StageCompleted || backlog.createCalls != 0 || cycle.Candidate == nil {
		t.Fatalf("cycle=%#v createCalls=%d", cycle, backlog.createCalls)
	}
}

func TestNoOpJudgmentCompletesWithoutBacklogMutation(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeProposal)
	service.Judge = dynamicJudge{call: func(harness.JudgmentRequest) domain.Judgment {
		return domain.Judgment{SchemaVersion: domain.JudgmentSchemaV1, NoOpReason: "Evidence is too weak."}
	}}
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Stage != domain.StageCompleted || backlog.createCalls != 0 {
		t.Fatalf("cycle=%#v createCalls=%d", cycle, backlog.createCalls)
	}
}

func TestForgedEvidenceDigestFailsBeforeMutation(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeProposal)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		j := selectedJudgment(t, request, service.Config.ProjectDir)
		j.Opportunities[0].Evidence[0].Digest = strings.Repeat("f", 64)
		return j
	}}
	_, err := service.Start(context.Background(), domain.ModeProposal)
	if err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("error = %v", err)
	}
	if backlog.createCalls != 0 {
		t.Fatalf("forged evidence mutated backlog")
	}
}

func TestRepositoryOutsidePortfolioFailsBeforeMutation(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeProposal)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		return selectedJudgment(t, request, "/outside/portfolio")
	}}
	_, err := service.Start(context.Background(), domain.ModeProposal)
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("error = %v", err)
	}
	if backlog.createCalls != 0 {
		t.Fatal("out-of-portfolio proposal mutated backlog")
	}
}

func mustObservationJSON(t *testing.T, service *Service, beads []adapters.Bead) []byte {
	t.Helper()
	store := service.Store
	artifact, err := store.WriteJSON("fixture", "beads", "beads.json", beads)
	if err != nil {
		t.Fatal(err)
	}
	observation := Observation{SchemaVersion: ObservationSchemaV1, CycleID: "fixture", Portfolio: "sylveste", Beads: beads, Artifacts: []domain.Artifact{artifact}}
	data, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLockContentionDoesNotCreateCycle(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	kernel.lockHeld = true
	_, err := service.Start(context.Background(), domain.ModeProposal)
	if !errors.Is(err, adapters.ErrLockHeld) {
		t.Fatalf("error = %v", err)
	}
	if backlog.createCalls != 0 || len(kernel.cycles) != 0 {
		t.Fatalf("lock contention had side effects")
	}
}

func TestNewCycleIDFormat(t *testing.T) {
	id, err := NewCycleID(time.Date(2026, 7, 13, 8, 9, 10, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "cycle-20260713T080910Z-") || len(id) != len("cycle-20260713T080910Z-")+8 {
		t.Fatalf("id = %q", id)
	}
}

func TestServiceConfigurationValidation(t *testing.T) {
	service, _, _ := testService(t, domain.ModeProposal)
	service.Config.AllowedRepositoryRoots = nil
	_, err := service.Start(context.Background(), domain.ModeProposal)
	if err == nil || !strings.Contains(err.Error(), "allowed repository") {
		t.Fatalf("error = %v", err)
	}
}

func ExampleObservation() {
	observation := Observation{SchemaVersion: ObservationSchemaV1, CycleID: "cycle-1", Portfolio: "sylveste"}
	data, _ := json.Marshal(observation)
	fmt.Println(strings.Contains(string(data), "remontoire.observation/v1"))
	// Output: true
}
