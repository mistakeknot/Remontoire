package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/cycle"
	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

type failedReplayKernel struct {
	cycles      map[string]domain.Cycle
	phase       string
	status      string
	receiptID   string
	receiptHash string
	advanceErr  error
}

func (k *failedReplayKernel) Health(context.Context) error { return nil }
func (k *failedReplayKernel) AcquireCycleLock(context.Context, string, string, string) error {
	return nil
}
func (k *failedReplayKernel) ReleaseCycleLock(context.Context, string, string) error { return nil }
func (k *failedReplayKernel) SetCycle(_ context.Context, value domain.Cycle) error {
	k.cycles[value.ID] = value
	return nil
}
func (k *failedReplayKernel) GetCycle(_ context.Context, id string) (domain.Cycle, error) {
	value, ok := k.cycles[id]
	if !ok {
		return domain.Cycle{}, adapters.ErrNotFound
	}
	return value, nil
}
func (k *failedReplayKernel) SetLatestCycle(context.Context, string, string) error { return nil }
func (k *failedReplayKernel) CreateCycleRun(context.Context, string, string, map[string]any) (string, error) {
	k.phase, k.status = "observe", "active"
	return "run-failed", nil
}
func (k *failedReplayKernel) AdvanceRun(context.Context, string) error {
	if k.advanceErr != nil {
		err := k.advanceErr
		k.advanceErr = nil
		return err
	}
	phases := []string{"observe", "rank", "propose", "execute", "review", "compound"}
	for i := 0; i < len(phases)-1; i++ {
		if k.phase == phases[i] {
			k.phase = phases[i+1]
			if k.phase == "compound" {
				k.status = "completed"
			}
			return nil
		}
	}
	return errors.New("run cannot advance")
}
func (k *failedReplayKernel) RunPhase(context.Context, string) (string, error) {
	return k.phase, nil
}
func (k *failedReplayKernel) RunStatus(context.Context, string) (string, string, error) {
	return k.phase, k.status, nil
}
func (k *failedReplayKernel) RecordReplayInput(context.Context, string, string, string, string, string) error {
	return nil
}
func (k *failedReplayKernel) RecordStageEvent(context.Context, string, string, domain.Stage, string) error {
	return nil
}
func (k *failedReplayKernel) Observation(context.Context, int) ([]adapters.Discovery, adapters.InterestProfile, error) {
	return nil, adapters.InterestProfile{}, nil
}
func (k *failedReplayKernel) ListOutcomes(context.Context, int) ([]domain.OutcomeSummary, error) {
	return nil, nil
}
func (k *failedReplayKernel) SetOutcome(context.Context, domain.OutcomeSummary) error { return nil }
func (k *failedReplayKernel) RecordDiscoveryFeedback(context.Context, string, string, string, string) error {
	return nil
}
func (k *failedReplayKernel) EmitReceipt(_ context.Context, _, _, hash string) (string, error) {
	k.receiptID, k.receiptHash = "receipt-failed", hash
	return k.receiptID, nil
}
func (k *failedReplayKernel) FindReceipt(_ context.Context, _, hash string) (string, error) {
	if k.receiptID == "" || hash != k.receiptHash {
		return "", adapters.ErrNotFound
	}
	return k.receiptID, nil
}
func (k *failedReplayKernel) VerifyReceipt(context.Context, string) error { return nil }

type failedReplayBacklog struct {
	items   []adapters.Bead
	listErr error
}

func (b failedReplayBacklog) List(context.Context) ([]adapters.Bead, error) {
	return append([]adapters.Bead(nil), b.items...), b.listErr
}
func (failedReplayBacklog) CreateExperiment(context.Context, string, string, domain.Candidate) (string, error) {
	return "", errors.New("unexpected experiment creation")
}
func (failedReplayBacklog) CreatePromotion(context.Context, string, string, domain.Candidate, int, string) (string, error) {
	return "", errors.New("unexpected promotion creation")
}
func (failedReplayBacklog) AddNote(context.Context, string, string) error {
	return errors.New("unexpected note")
}
func (failedReplayBacklog) Close(context.Context, string, string) error {
	return errors.New("unexpected close")
}

type failedReplayPolicy struct{}

func (failedReplayPolicy) Weights(context.Context) (map[string]int, bool) { return nil, false }

type failedReplayJudge struct {
	call func(harness.JudgmentRequest) domain.Judgment
}

func (j failedReplayJudge) Judge(_ context.Context, request harness.JudgmentRequest) (domain.Judgment, harness.Metadata, error) {
	if j.call == nil {
		return domain.Judgment{}, harness.Metadata{}, errors.New("unexpected judgment")
	}
	return j.call(request), harness.Metadata{Backend: "codex"}, nil
}

func TestEarlyFailureCompoundReceiptReplaysOffline(t *testing.T) {
	project := t.TempDir()
	store := cycle.FileStore{Root: filepath.Join(project, ".remontoire")}
	kernel := &failedReplayKernel{cycles: map[string]domain.Cycle{}}
	now := time.Date(2026, 7, 13, 19, 0, 0, 0, time.UTC)
	service := &cycle.Service{
		Config: cycle.Config{
			Portfolio: "sylveste", ProjectDir: project, ArtifactRoot: store.Root,
			AllowedRepositoryRoots: []string{project}, DefaultMode: domain.ModeProposal,
		},
		Kernel: kernel, Backlog: failedReplayBacklog{listErr: errors.New("canonical backlog unavailable")}, Policy: failedReplayPolicy{}, Judge: failedReplayJudge{},
		Store: store, Now: func() time.Time { return now }, NewID: func(time.Time) (string, error) { return "cycle-actual-failure", nil },
	}

	failed, startErr := service.Start(context.Background(), domain.ModeProposal)
	if startErr == nil || failed.Stage != domain.StageFailed {
		t.Fatalf("cycle=%#v error=%v", failed, startErr)
	}
	signed, err := service.Compound(context.Background(), failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if signed.SignedReceiptID == "" || signed.Stage != domain.StageFailed {
		t.Fatalf("signed cycle = %#v", signed)
	}

	result, err := (&Application{Store: store}).ReplayReceipt(failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.InputsVerified != 0 || result.OutputsVerified != 0 || result.SignatureVerified {
		t.Fatalf("replay result = %#v", result)
	}
}

func TestDuplicateNoOpCompoundReceiptReplaysFromProposalSnapshot(t *testing.T) {
	project := t.TempDir()
	store := cycle.FileStore{Root: filepath.Join(project, ".remontoire")}
	kernel := &failedReplayKernel{cycles: map[string]domain.Cycle{}}
	now := time.Date(2026, 7, 13, 19, 30, 0, 0, time.UTC)
	candidate := domain.Candidate{
		Title: "Measure roadmap cache", Summary: "Resolve bounded roadmap cache uncertainty.", Project: "Remontoire", Priority: 4,
		Impact: 0.8, Uncertainty: 0.7, Cost: 0.2, Risk: 0.1, PolicyFit: 0.9,
		Contract: domain.EvidenceContract{
			SchemaVersion: domain.ContractSchemaV1, Hypothesis: "Caching reduces refresh latency.", Falsifier: "Latency does not improve.",
			Repository: project, AllowedPaths: []string{"internal/cache"},
			Metric:    domain.Metric{Name: "latency", Unit: "ms", Direction: domain.DirectionMinimize, Source: domain.MetricSourceWallDurationMS, Baseline: 100, Target: 80},
			Benchmark: []string{"go", "test", "./internal/cache"}, Budget: domain.Budget{MaxDurationSeconds: 60, MaxTurns: 4, MaxCostUSD: 1},
			StopConditions: []string{"benchmark fails"}, Executor: "codex", PromotionCriteria: "Latency is below 80 ms.", ClosureCriteria: "Latency does not improve.",
		},
	}
	fingerprint := domain.FingerprintCandidate(candidate)
	backlog := failedReplayBacklog{items: []adapters.Bead{{
		ID: "Revel-existing", Title: "Existing experiment", Status: "open", Priority: 4, IssueType: "task",
		Labels: []string{"remontoire:fingerprint:" + fingerprint},
	}}}
	judge := failedReplayJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		var observation cycle.Observation
		if err := json.Unmarshal(request.Observation, &observation); err != nil {
			t.Fatalf("decode observation: %v", err)
		}
		var beadsDigest string
		for _, artifact := range observation.Artifacts {
			if artifact.Kind == "beads" {
				beadsDigest = artifact.Digest
			}
		}
		selected := 0
		value := candidate
		value.Evidence = []domain.EvidenceRef{{Kind: "bead", ID: "Revel-existing", Digest: beadsDigest}}
		return domain.Judgment{SchemaVersion: domain.JudgmentSchemaV1, Opportunities: []domain.Candidate{value}, SelectedIndex: &selected}
	}}
	service := &cycle.Service{
		Config: cycle.Config{
			Portfolio: "sylveste", ProjectDir: project, ArtifactRoot: store.Root,
			AllowedRepositoryRoots: []string{project}, DefaultMode: domain.ModeProposal,
		},
		Kernel: kernel, Backlog: backlog, Policy: failedReplayPolicy{}, Judge: judge,
		Store: store, Now: func() time.Time { return now }, NewID: func(time.Time) (string, error) { return "cycle-actual-duplicate", nil },
	}

	completed, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Stage != domain.StageCompleted || completed.NoOpReason != cycle.NoOpReasonDuplicateFingerprint {
		t.Fatalf("completed cycle = %#v", completed)
	}
	signed, err := service.Compound(context.Background(), completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Application{Store: store}).ReplayReceipt(signed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.ContractHash != signed.ContractHash || result.SignatureVerified {
		t.Fatalf("replay result = %#v", result)
	}
}

func TestFailedNoOpJudgmentBeforeResolutionReplaysOffline(t *testing.T) {
	project := t.TempDir()
	store := cycle.FileStore{Root: filepath.Join(project, ".remontoire")}
	kernel := &failedReplayKernel{cycles: map[string]domain.Cycle{}, advanceErr: errors.New("rank advance unavailable")}
	now := time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC)
	judge := failedReplayJudge{call: func(harness.JudgmentRequest) domain.Judgment {
		return domain.Judgment{SchemaVersion: domain.JudgmentSchemaV1, NoOpReason: "No bounded opportunity clears the threshold."}
	}}
	service := &cycle.Service{
		Config: cycle.Config{
			Portfolio: "sylveste", ProjectDir: project, ArtifactRoot: store.Root,
			AllowedRepositoryRoots: []string{project}, DefaultMode: domain.ModeProposal,
		},
		Kernel: kernel, Backlog: failedReplayBacklog{}, Policy: failedReplayPolicy{}, Judge: judge,
		Store: store, Now: func() time.Time { return now }, NewID: func(time.Time) (string, error) { return "cycle-failed-no-op", nil },
	}

	failed, startErr := service.Start(context.Background(), domain.ModeProposal)
	if startErr == nil || failed.Stage != domain.StageFailed || failed.Judgment == nil || failed.NoOpReason != "" {
		t.Fatalf("failed cycle=%#v error=%v", failed, startErr)
	}
	if _, err := service.Compound(context.Background(), failed.ID); err != nil {
		t.Fatal(err)
	}
	result, err := (&Application{Store: store}).ReplayReceipt(failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.ContractHash != "" {
		t.Fatalf("replay result = %#v", result)
	}
}
