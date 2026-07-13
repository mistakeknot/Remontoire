package cycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

type fakeKernel struct {
	cycles               map[string]domain.Cycle
	latest               string
	lockHeld             bool
	setCalls             int
	events               []domain.Stage
	eventKeys            map[string]bool
	eventErrors          map[domain.Stage]error
	replay               []string
	advanceCalls         int
	phase                string
	runStatus            string
	advanceErrAt         int
	advanceCommitErr     bool
	advanceErr           error
	onAdvance            func(*fakeKernel)
	outcomes             map[string]domain.OutcomeSummary
	receiptCalls         int
	findCalls            int
	findReceiptID        string
	emitErr              error
	emitStoresOnError    bool
	verifyCalls          int
	feedbackCalls        int
	feedbackWrites       int
	feedbackErr          error
	feedbackStoreErr     bool
	feedbackKeys         map[string]bool
	respectContext       bool
	failedSetHasDeadline bool
	releaseHasDeadline   bool
	latestErr            error
	createRunErr         error
	createRunCalls       int
}

func newFakeKernel() *fakeKernel {
	return &fakeKernel{
		cycles: map[string]domain.Cycle{}, outcomes: map[string]domain.OutcomeSummary{},
		eventKeys: map[string]bool{}, eventErrors: map[domain.Stage]error{}, feedbackKeys: map[string]bool{}, phase: "observe", runStatus: "active",
	}
}

func (k *fakeKernel) Health(context.Context) error { return nil }
func (k *fakeKernel) AcquireCycleLock(context.Context, string, string, string) error {
	if k.lockHeld {
		return adapters.ErrLockHeld
	}
	k.lockHeld = true
	return nil
}
func (k *fakeKernel) ReleaseCycleLock(ctx context.Context, _ string, _ string) error {
	_, k.releaseHasDeadline = ctx.Deadline()
	k.lockHeld = false
	return nil
}
func (k *fakeKernel) SetCycle(ctx context.Context, cycle domain.Cycle) error {
	if cycle.Stage == domain.StageFailed {
		_, k.failedSetHasDeadline = ctx.Deadline()
	}
	if k.respectContext && ctx.Err() != nil {
		return ctx.Err()
	}
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
	if k.latestErr != nil {
		return k.latestErr
	}
	k.latest = id
	return nil
}
func (k *fakeKernel) CreateCycleRun(context.Context, string, string, map[string]any) (string, error) {
	k.createRunCalls++
	if k.createRunErr != nil {
		return "", k.createRunErr
	}
	k.phase = "observe"
	k.runStatus = "active"
	return "run-1", nil
}
func (k *fakeKernel) AdvanceRun(context.Context, string) error {
	k.advanceCalls++
	if k.onAdvance != nil {
		k.onAdvance(k)
	}
	shouldFail := k.advanceErr != nil && k.advanceCalls == k.advanceErrAt
	if !shouldFail || k.advanceCommitErr {
		phases := []string{"observe", "rank", "propose", "execute", "review", "compound"}
		for i := range phases[:len(phases)-1] {
			if k.phase == phases[i] {
				k.phase = phases[i+1]
				if k.phase == "compound" {
					k.runStatus = "completed"
				}
				break
			}
		}
	}
	if shouldFail {
		err := k.advanceErr
		k.advanceErr = nil
		return err
	}
	return nil
}
func (k *fakeKernel) RunPhase(context.Context, string) (string, error) {
	return k.phase, nil
}
func (k *fakeKernel) RunStatus(context.Context, string) (string, string, error) {
	return k.phase, k.runStatus, nil
}
func (k *fakeKernel) RecordReplayInput(_ context.Context, _, kind, key, _, _ string) error {
	k.replay = append(k.replay, kind+":"+key)
	return nil
}
func (k *fakeKernel) RecordStageEvent(ctx context.Context, _, _ string, stage domain.Stage, cycleID string) error {
	if k.respectContext && ctx.Err() != nil {
		return ctx.Err()
	}
	if err := k.eventErrors[stage]; err != nil {
		return err
	}
	key := cycleID + ":" + string(stage)
	if !k.eventKeys[key] {
		k.eventKeys[key] = true
		k.events = append(k.events, stage)
	}
	return nil
}
func (k *fakeKernel) Observation(context.Context, int) ([]adapters.Discovery, adapters.InterestProfile, error) {
	return []adapters.Discovery{{ID: "disc-1", Source: "interject", Title: "Roadmap refresh overhead", Status: "scored", RelevanceScore: 0.8}}, adapters.InterestProfile{KeywordWeights: `{}`, SourceWeights: `{}`}, nil
}
func (k *fakeKernel) ListOutcomes(context.Context, int) ([]domain.OutcomeSummary, error) {
	result := make([]domain.OutcomeSummary, 0, len(k.outcomes))
	for _, outcome := range k.outcomes {
		result = append(result, outcome)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CycleID < result[j].CycleID })
	return result, nil
}
func (k *fakeKernel) SetOutcome(_ context.Context, outcome domain.OutcomeSummary) error {
	k.outcomes[outcome.CycleID] = outcome
	return nil
}
func (k *fakeKernel) RecordDiscoveryFeedback(_ context.Context, _, _, _, idempotencyKey string) error {
	k.feedbackCalls++
	if k.feedbackKeys[idempotencyKey] {
		return nil
	}
	if k.feedbackErr != nil {
		if k.feedbackStoreErr {
			k.feedbackKeys[idempotencyKey] = true
			k.feedbackWrites++
		}
		err := k.feedbackErr
		k.feedbackErr = nil
		return err
	}
	k.feedbackKeys[idempotencyKey] = true
	k.feedbackWrites++
	return nil
}
func (k *fakeKernel) EmitReceipt(context.Context, string, string, string) (string, error) {
	k.receiptCalls++
	if k.emitErr != nil {
		if k.emitStoresOnError {
			k.findReceiptID = "receipt-1"
		}
		return "", k.emitErr
	}
	k.findReceiptID = "receipt-1"
	return "receipt-1", nil
}
func (k *fakeKernel) FindReceipt(context.Context, string, string) (string, error) {
	k.findCalls++
	if k.findReceiptID == "" {
		return "", adapters.ErrNotFound
	}
	return k.findReceiptID, nil
}
func (k *fakeKernel) VerifyReceipt(context.Context, string) error {
	k.verifyCalls++
	return nil
}

type fakeBacklog struct {
	items             []adapters.Bead
	listErr           error
	listCalls         int
	createCalls       int
	created           domain.Candidate
	promotionCalls    int
	promotionEvidence string
	closeCalls        int
	noteCalls         int
}

func (b *fakeBacklog) List(context.Context) ([]adapters.Bead, error) {
	b.listCalls++
	if b.listErr != nil {
		return nil, b.listErr
	}
	return append([]adapters.Bead(nil), b.items...), nil
}
func (b *fakeBacklog) CreateExperiment(_ context.Context, cycleID, fingerprint string, candidate domain.Candidate) (string, error) {
	b.createCalls++
	b.created = candidate
	id := "Revel-experiment"
	b.items = append(b.items, adapters.Bead{
		ID: id, Title: candidate.Title, Status: "open", Priority: 4,
		Labels: []string{"remontoire-experiment", "remontoire:cycle:" + cycleID, "remontoire:fingerprint:" + fingerprint},
	})
	return id, nil
}
func (b *fakeBacklog) CreatePromotion(_ context.Context, cycleID, experimentID string, candidate domain.Candidate, priority int, evidence string) (string, error) {
	b.promotionCalls++
	b.promotionEvidence = evidence
	id := "Revel-promotion"
	b.items = append(b.items, adapters.Bead{
		ID: id, Title: candidate.Title, Status: "open", Priority: priority,
		Labels: []string{"remontoire-promotion", "remontoire:cycle:" + cycleID},
	})
	return id, nil
}
func (b *fakeBacklog) AddNote(context.Context, string, string) error {
	b.noteCalls++
	return nil
}
func (b *fakeBacklog) Close(_ context.Context, beadID, _ string) error {
	b.closeCalls++
	for i := range b.items {
		if b.items[i].ID == beadID {
			b.items[i].Status = "closed"
		}
	}
	return nil
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

type cancelingJudge struct {
	cancel context.CancelFunc
}

type failingJudge struct {
	calls int
}

func (j *failingJudge) Judge(context.Context, harness.JudgmentRequest) (domain.Judgment, harness.Metadata, error) {
	j.calls++
	return domain.Judgment{}, harness.Metadata{}, errors.New("judge must not be invoked")
}

func (j cancelingJudge) Judge(context.Context, harness.JudgmentRequest) (domain.Judgment, harness.Metadata, error) {
	j.cancel()
	return domain.Judgment{}, harness.Metadata{}, context.Canceled
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

func TestResumeObservationFromNewAfterLatestPointerFailure(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	kernel.latestErr = errors.New("latest pointer unavailable")
	interrupted, err := service.Start(context.Background(), domain.ModeProposal)
	if err == nil || interrupted.Stage != domain.StageNew || backlog.createCalls != 0 {
		t.Fatalf("cycle=%#v error=%v creates=%d", interrupted, err, backlog.createCalls)
	}
	kernel.latestErr = nil

	resumed, err := service.ResumeObservation(context.Background(), interrupted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.StageAwaitingApproval || backlog.createCalls != 1 || kernel.latest != interrupted.ID {
		t.Fatalf("cycle=%#v creates=%d latest=%q", resumed, backlog.createCalls, kernel.latest)
	}
}

func TestStartPersistsNewCycleBeforeRunCreationAndResumes(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	kernel.createRunErr = context.DeadlineExceeded
	interrupted, err := service.Start(context.Background(), domain.ModeProposal)
	if !errors.Is(err, context.DeadlineExceeded) || interrupted.ID == "" || interrupted.Stage != domain.StageNew || interrupted.RunID != "" {
		t.Fatalf("cycle=%#v error=%v", interrupted, err)
	}
	stored := kernel.cycles[interrupted.ID]
	if stored.Stage != domain.StageNew || stored.RunID != "" || kernel.latest != interrupted.ID || backlog.createCalls != 0 {
		t.Fatalf("stored=%#v latest=%q creates=%d", stored, kernel.latest, backlog.createCalls)
	}
	kernel.createRunErr = nil

	resumed, err := service.ResumeObservation(context.Background(), interrupted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.StageAwaitingApproval || resumed.RunID != "run-1" || kernel.createRunCalls != 2 || backlog.createCalls != 1 {
		t.Fatalf("cycle=%#v run creates=%d backlog creates=%d", resumed, kernel.createRunCalls, backlog.createCalls)
	}
}

func TestResumeObservationRepairsInterruptedObservingEvent(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	kernel.eventErrors[domain.StageObserving] = errors.New("event store unavailable")
	interrupted, err := service.Start(context.Background(), domain.ModeProposal)
	if err == nil || interrupted.Stage != domain.StageObserving || backlog.createCalls != 0 {
		t.Fatalf("cycle=%#v error=%v creates=%d", interrupted, err, backlog.createCalls)
	}
	delete(kernel.eventErrors, domain.StageObserving)

	resumed, err := service.ResumeObservation(context.Background(), interrupted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.StageAwaitingApproval || stageCount(kernel.events, domain.StageObserving) != 1 || backlog.createCalls != 1 {
		t.Fatalf("cycle=%#v events=%#v creates=%d", resumed, kernel.events, backlog.createCalls)
	}
}

func TestResumeObservationFinishesRankedShadowWithoutBacklogMutation(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeShadow)
	kernel.eventErrors[domain.StageRanked] = errors.New("event store unavailable")
	interrupted, err := service.Start(context.Background(), domain.ModeShadow)
	if err == nil || interrupted.Stage != domain.StageRanked || backlog.createCalls != 0 {
		t.Fatalf("cycle=%#v error=%v creates=%d", interrupted, err, backlog.createCalls)
	}
	delete(kernel.eventErrors, domain.StageRanked)

	resumed, err := service.ResumeObservation(context.Background(), interrupted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.StageCompleted || backlog.createCalls != 0 {
		t.Fatalf("cycle=%#v creates=%d", resumed, backlog.createCalls)
	}
}

func TestResumeObservationFinishesRankedNoOp(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	service.Judge = dynamicJudge{call: func(harness.JudgmentRequest) domain.Judgment {
		return domain.Judgment{SchemaVersion: domain.JudgmentSchemaV1, NoOpReason: "No bounded opportunity."}
	}}
	kernel.eventErrors[domain.StageRanked] = errors.New("event store unavailable")
	interrupted, err := service.Start(context.Background(), domain.ModeProposal)
	if err == nil || interrupted.Stage != domain.StageRanked {
		t.Fatalf("cycle=%#v error=%v", interrupted, err)
	}
	delete(kernel.eventErrors, domain.StageRanked)

	resumed, err := service.ResumeObservation(context.Background(), interrupted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.StageCompleted || backlog.createCalls != 0 {
		t.Fatalf("cycle=%#v creates=%d", resumed, backlog.createCalls)
	}
}

func TestResumeObservationUsesStoredSnapshotWithoutLiveSourceReads(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeShadow)
	completed, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil {
		t.Fatal(err)
	}
	replayCount := len(kernel.replay)
	completed.Stage = domain.StageObserving
	completed.Judgment = nil
	completed.Candidate = nil
	completed.CandidateHash = ""
	completed.ContractHash = ""
	kept := completed.Artifacts[:0]
	for _, artifact := range completed.Artifacts {
		if artifact.Kind != "judgment" {
			kept = append(kept, artifact)
		}
	}
	completed.Artifacts = kept
	delete(completed.IdempotencyKeys, "run:rank")
	delete(completed.IdempotencyKeys, "event:no_op")
	delete(completed.IdempotencyKeys, "event:completed")
	judgmentPath, err := service.Store.Path(completed.ID, "judgment.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(judgmentPath); err != nil {
		t.Fatal(err)
	}
	kernel.cycles[completed.ID] = completed
	kernel.phase = "observe"
	kernel.runStatus = "active"
	backlog.listErr = errors.New("live backlog must not be read")

	resumed, err := service.ResumeObservation(context.Background(), completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.StageCompleted || resumed.Judgment == nil || resumed.Candidate == nil || len(kernel.replay) != replayCount {
		t.Fatalf("resumed cycle = %#v", resumed)
	}
}

func TestResumeObservationFailsClosedAfterPartialCapture(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	cycle := domain.Cycle{
		SchemaVersion: domain.CycleSchemaV1, ID: "cycle-1", RunID: "run-1", Portfolio: "sylveste",
		Mode: domain.ModeProposal, Stage: domain.StageObserving, CreatedAt: service.now(), UpdatedAt: service.now(),
		IdempotencyKeys: map[string]string{"observation:capture": "started"},
	}
	kernel.cycles[cycle.ID] = cycle

	failed, err := service.ResumeObservation(context.Background(), cycle.ID)
	if !errors.Is(err, ErrObservationIndeterminate) || failed.Stage != domain.StageFailed || backlog.listCalls != 0 || backlog.createCalls != 0 {
		t.Fatalf("cycle=%#v error=%v lists=%d creates=%d", failed, err, backlog.listCalls, backlog.createCalls)
	}
}

func TestResumeObservationRejectsMutatedCanonicalComposite(t *testing.T) {
	service, kernel, _ := testService(t, domain.ModeShadow)
	completed, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil {
		t.Fatal(err)
	}
	completed.Stage = domain.StageObserving
	delete(completed.IdempotencyKeys, "run:rank")
	kernel.cycles[completed.ID] = completed
	kernel.phase = "observe"
	path, err := service.Store.Path(completed.ID, "observation.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	failed, err := service.ResumeObservation(context.Background(), completed.ID)
	if err == nil || failed.Stage != domain.StageFailed || !strings.Contains(err.Error(), "observation artifact digest changed") {
		t.Fatalf("cycle=%#v error=%v", failed, err)
	}
	count := 0
	for _, artifact := range failed.Artifacts {
		if artifact.Kind == "observation" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("observation artifact count = %d", count)
	}
}

func TestResumeObservationReusesCanonicalJudgmentWithoutReinvocation(t *testing.T) {
	service, kernel, _ := testService(t, domain.ModeShadow)
	completed, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil {
		t.Fatal(err)
	}
	completed.Stage = domain.StageObserving
	delete(completed.IdempotencyKeys, "run:rank")
	delete(completed.IdempotencyKeys, "event:no_op")
	delete(completed.IdempotencyKeys, "event:completed")
	kernel.cycles[completed.ID] = completed
	kernel.phase = "observe"
	kernel.runStatus = "active"
	judge := &failingJudge{}
	service.Judge = judge

	resumed, err := service.ResumeObservation(context.Background(), completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.StageCompleted || judge.calls != 0 {
		t.Fatalf("cycle=%#v judge calls=%d", resumed, judge.calls)
	}
}

func TestResumeObservationRejectsMutatedCanonicalJudgment(t *testing.T) {
	service, kernel, _ := testService(t, domain.ModeShadow)
	completed, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil {
		t.Fatal(err)
	}
	completed.Stage = domain.StageObserving
	delete(completed.IdempotencyKeys, "run:rank")
	kernel.cycles[completed.ID] = completed
	kernel.phase = "observe"
	path, err := service.Store.Path(completed.ID, "judgment.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	failed, err := service.ResumeObservation(context.Background(), completed.ID)
	if err == nil || failed.Stage != domain.StageFailed || !strings.Contains(err.Error(), "judgment artifact digest changed") {
		t.Fatalf("cycle=%#v error=%v", failed, err)
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

func TestRankAdvanceTimeoutAfterCommitIsReconciled(t *testing.T) {
	service, kernel, _ := testService(t, domain.ModeProposal)
	kernel.advanceErrAt = 1
	kernel.advanceCommitErr = true
	kernel.advanceErr = errors.New("rank response timed out")

	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Stage != domain.StageAwaitingApproval || cycle.IdempotencyKeys["run:rank"] != "completed" || kernel.phase != "propose" {
		t.Fatalf("cycle=%#v phase=%s", cycle, kernel.phase)
	}
}

func TestProposalResumeReconcilesCommittedRunAdvance(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	cycle.Stage = domain.StageRanked
	cycle.ExperimentBeadID = ""
	cycle.IdempotencyKeys["run:propose"] = "started"
	kernel.cycles[cycle.ID] = cycle
	kernel.phase = "propose"
	before := kernel.advanceCalls

	resumed, err := service.ResumeProposal(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != domain.StageAwaitingApproval || resumed.IdempotencyKeys["run:propose"] != "completed" || kernel.advanceCalls != before || backlog.createCalls != 1 {
		t.Fatalf("resumed=%#v advances=%d before=%d creates=%d", resumed, kernel.advanceCalls, before, backlog.createCalls)
	}
}

func TestDuplicateFingerprintCompletesAsNoOp(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
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
	if cycle.Stage != domain.StageCompleted || backlog.createCalls != 0 || !strings.Contains(cycle.NoOpReason, "duplicate") {
		t.Fatalf("cycle=%#v createCalls=%d", cycle, backlog.createCalls)
	}
	cycle.Stage = domain.StageRanked
	cycle.NoOpReason = ""
	kernel.cycles[cycle.ID] = cycle
	backlog.listErr = errors.New("live backlog unavailable")
	cycle, err = service.ResumeObservation(context.Background(), cycle.ID)
	if err != nil || cycle.Stage != domain.StageCompleted || cycle.NoOpReason != NoOpReasonDuplicateFingerprint {
		t.Fatalf("snapshot resume cycle=%#v error=%v", cycle, err)
	}
	backlog.listErr = nil
	signed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored domain.Judgment
	if err := service.Store.ReadJSON(cycle.ID, "judgment.json", &stored); err != nil {
		t.Fatal(err)
	}
	if signed.Judgment == nil || signed.Judgment.SelectedIndex == nil || signed.Candidate == nil || !strings.Contains(signed.NoOpReason, "duplicate") || !reflect.DeepEqual(stored, *signed.Judgment) {
		t.Fatalf("signed judgment=%#v candidate=%#v stored=%#v", signed.Judgment, signed.Candidate, stored)
	}
}

func TestProposalSnapshotValidationHashesTheDecodedBytes(t *testing.T) {
	service, _, _ := testService(t, domain.ModeProposal)
	cycle := domain.Cycle{ID: "cycle-snapshot", Artifacts: nil}
	path, err := service.Store.Path(cycle.ID, "proposal-backlog.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[{"id":"disk-version"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	decodedBytes := []byte(`[{"id":"decoded-version"}]`)
	beads, artifact, err := service.validateProposalBacklogSnapshot(cycle, path, decodedBytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(decodedBytes)
	if len(beads) != 1 || beads[0].ID != "decoded-version" || artifact.Digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("beads=%#v artifact=%#v", beads, artifact)
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

func TestNoOpJudgmentRejectsUnboundOpportunityEvidence(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeProposal)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		judgment.SelectedIndex = nil
		judgment.NoOpReason = "No opportunity clears the execution threshold."
		judgment.Opportunities[0].Evidence[0].ID = "Revel-missing"
		return judgment
	}}

	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err == nil || cycle.Stage != domain.StageFailed || !strings.Contains(err.Error(), "not in the observation") {
		t.Fatalf("cycle=%#v error=%v", cycle, err)
	}
	if backlog.createCalls != 0 {
		t.Fatalf("backlog creates = %d", backlog.createCalls)
	}
}

func TestObservationSnapshotsRoadmapForOfflineReplay(t *testing.T) {
	service, _, _ := testService(t, domain.ModeShadow)
	roadmapPath := filepath.Join(t.TempDir(), "roadmap.json")
	original := []byte("{\"version\":1}\n")
	if err := os.WriteFile(roadmapPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	service.Config.RoadmapPath = roadmapPath
	cycle, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot domain.Artifact
	for _, artifact := range cycle.Artifacts {
		if artifact.Kind == "roadmap" {
			snapshot = artifact
		}
	}
	if snapshot.Path == "" || snapshot.Path == roadmapPath || !strings.HasPrefix(snapshot.Path, filepath.Join(service.Config.ArtifactRoot, "cycles", cycle.ID)+string(filepath.Separator)) {
		t.Fatalf("roadmap snapshot = %#v", snapshot)
	}
	if err := os.WriteFile(roadmapPath, []byte("{\"version\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(snapshot.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(original) {
		t.Fatalf("snapshot changed with live roadmap: %q", stored)
	}
}

func TestCancellationPersistsFailedCycleForReceiptRecovery(t *testing.T) {
	service, kernel, _ := testService(t, domain.ModeProposal)
	kernel.respectContext = true
	ctx, cancel := context.WithCancel(context.Background())
	service.Judge = cancelingJudge{cancel: cancel}

	failed, err := service.Start(ctx, domain.ModeProposal)
	if !errors.Is(err, context.Canceled) || failed.Stage != domain.StageFailed {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	stored := kernel.cycles[failed.ID]
	if stored.Stage != domain.StageFailed || stored.Failure == "" || !kernel.failedSetHasDeadline || !kernel.releaseHasDeadline {
		t.Fatalf("canonical failed cycle = %#v", stored)
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

func TestJudgmentRejectsRepositorySymlinkEscape(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeProposal)
	link := filepath.Join(service.Config.AllowedRepositoryRoots[0], "outside-repository")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		return selectedJudgment(t, request, link)
	}}

	failed, err := service.Start(context.Background(), domain.ModeProposal)
	if err == nil || failed.Stage != domain.StageFailed || !strings.Contains(err.Error(), "outside allowed repository roots") {
		t.Fatalf("cycle=%#v error=%v", failed, err)
	}
	if backlog.createCalls != 0 {
		t.Fatalf("backlog creates = %d", backlog.createCalls)
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
