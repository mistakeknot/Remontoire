package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/cycle"
	"github.com/mistakeknot/Remontoire/internal/domain"
	receiptpkg "github.com/mistakeknot/Remontoire/internal/receipt"
)

func replayFixture(t *testing.T, application *Application) (domain.Receipt, string) {
	t.Helper()
	repository := application.Config.ProjectDir
	contract := domain.EvidenceContract{
		SchemaVersion: domain.ContractSchemaV1, Hypothesis: "Cache refreshes reduce roadmap latency.",
		Falsifier: "The measured latency does not improve.", Repository: repository,
		AllowedPaths:   []string{"internal/cache"},
		Metric:         domain.Metric{Name: "latency", Unit: "ms", Direction: domain.DirectionMinimize, Source: domain.MetricSourceWallDurationMS, Baseline: 100, Target: 80},
		Benchmark:      []string{"go", "test", "./internal/cache"},
		Budget:         domain.Budget{MaxDurationSeconds: 60, MaxTurns: 4, MaxCostUSD: 1},
		StopConditions: []string{"benchmark fails"}, Executor: "codex",
		PromotionCriteria: "Latency is below 80 ms.", ClosureCriteria: "Latency does not improve.",
		ArtifactPaths: []string{"internal/cache/cache_test.go"},
	}
	candidate := domain.Candidate{
		Title: "Measure roadmap cache", Summary: "Bound the cache uncertainty.", Project: "interpath", Priority: 4,
		Impact: 0.8, Uncertainty: 0.7, Cost: 0.2, Risk: 0.1, PolicyFit: 0.9,
		Contract: contract,
	}
	index := 0
	judgment := domain.Judgment{SchemaVersion: domain.JudgmentSchemaV1, Opportunities: []domain.Candidate{candidate}, SelectedIndex: &index}
	contractHash, err := domain.HashContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	beadsValue := []adapters.Bead{{ID: "Revel-source", Title: "Canonical source", Status: "open", Priority: 2, IssueType: "task"}}
	beads, err := application.Store.WriteJSON("cycle-replay", "beads", "beads.json", beadsValue)
	if err != nil {
		t.Fatal(err)
	}
	discoveries, err := application.Store.WriteJSON("cycle-replay", "discoveries", "discoveries.json", []adapters.Discovery{})
	if err != nil {
		t.Fatal(err)
	}
	profileValue := adapters.InterestProfile{KeywordWeights: `{}`, SourceWeights: `{}`, UpdatedAt: 1}
	profile, err := application.Store.WriteJSON("cycle-replay", "interest-profile", "interest-profile.json", profileValue)
	if err != nil {
		t.Fatal(err)
	}
	ockham, err := application.Store.WriteJSON("cycle-replay", "ockham", "ockham.json", map[string]any{"weights": map[string]int{"research": 1}, "degraded": false})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := application.Store.WriteJSON("cycle-replay", "outcomes", "outcomes.json", []domain.OutcomeSummary{})
	if err != nil {
		t.Fatal(err)
	}
	roadmap, err := application.Store.WriteBytes("cycle-replay", "roadmap", "roadmap.json", []byte("{\"roadmap\":true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 18, 0, 0, 0, time.UTC)
	observationValue := cycle.Observation{
		SchemaVersion: cycle.ObservationSchemaV1, CycleID: "cycle-replay", Portfolio: "sylveste", CapturedAt: now.Add(-2 * time.Minute),
		Beads: beadsValue, Discoveries: []adapters.Discovery{}, InterestProfile: profileValue,
		OckhamWeights: map[string]int{"research": 1}, PriorOutcomes: []domain.OutcomeSummary{}, RoadmapDigest: roadmap.Digest,
		Artifacts: []domain.Artifact{beads, discoveries, profile, ockham, outcomes, roadmap},
	}
	observation, err := application.Store.WriteJSON("cycle-replay", "observation", "observation.json", observationValue)
	if err != nil {
		t.Fatal(err)
	}
	roadmapOutput, err := application.Store.WriteBytes("cycle-replay", "roadmap-output", "roadmap-output.json", []byte("{\"roadmap\":\"generated\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	candidate.Evidence = []domain.EvidenceRef{{Kind: "roadmap", ID: "roadmap", Digest: roadmap.Digest}}
	judgment.Opportunities[0] = candidate
	judgmentArtifact, err := application.Store.WriteJSON("cycle-replay", "judgment", "judgment.json", judgment)
	if err != nil {
		t.Fatal(err)
	}
	value, err := receiptpkg.Build(domain.Cycle{
		SchemaVersion: domain.CycleSchemaV1, ID: "cycle-replay", RunID: "run-replay", Portfolio: "sylveste",
		Mode: domain.ModeProposal, Stage: domain.StageCompleted, CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
		Judgment: &judgment, Candidate: &candidate, CandidateHash: domain.FingerprintCandidate(candidate), ContractHash: contractHash,
		Artifacts: []domain.Artifact{beads, discoveries, profile, ockham, outcomes, observation, roadmap, roadmapOutput, judgmentArtifact}, IdempotencyKeys: map[string]string{"run:terminal": "completed"},
		RoadmapDigest: roadmapOutput.Digest,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := receiptpkg.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Store.WriteBytes("cycle-replay", "receipt", "receipt.json", data); err != nil {
		t.Fatal(err)
	}
	return value, observation.Path
}

func rewriteReplayReceipt(t *testing.T, application *Application, cycleValue domain.Cycle, terminalAt time.Time) domain.Receipt {
	t.Helper()
	value, err := receiptpkg.Build(cycleValue, terminalAt)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := receiptpkg.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Store.WriteBytes(value.Cycle.ID, "receipt", "receipt.json", data); err != nil {
		t.Fatal(err)
	}
	return value
}

func replaceReplayArtifact(t *testing.T, artifacts []domain.Artifact, updated domain.Artifact) []domain.Artifact {
	t.Helper()
	result := append([]domain.Artifact(nil), artifacts...)
	for i := range result {
		if result[i].Kind == updated.Kind {
			result[i] = updated
			return result
		}
	}
	t.Fatalf("artifact %s not found", updated.Kind)
	return nil
}

func TestReceiptShowAndOfflineReplayUseOnlyStoredArtifacts(t *testing.T) {
	engine := &fakeEngine{}
	application := testApplication(t, engine)
	wantReceipt, _ := replayFixture(t, application)
	if err := os.WriteFile(application.Config.RoadmapPath, []byte("{\"roadmap\":\"later-cycle\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, shown, stderr := runJSON(t, application, "receipt", "show", "cycle-replay", "--json")
	if code != 0 || stderr != "" || shown["decision_hash"] != wantReceipt.DecisionHash {
		t.Fatalf("show code=%d output=%#v stderr=%q", code, shown, stderr)
	}
	code, replayed, stderr := runJSON(t, application, "receipt", "replay", "cycle-replay", "--json")
	if code != 0 || stderr != "" || replayed["verified"] != true || replayed["signature_verified"] != false ||
		replayed["verification_scope"] != "stored-content-self-consistency" || replayed["decision_hash"] != wantReceipt.DecisionHash || replayed["contract_hash"] != wantReceipt.Cycle.ContractHash {
		t.Fatalf("replay code=%d output=%#v stderr=%q", code, replayed, stderr)
	}
	if len(engine.serviceCalls) != 0 || len(engine.stateCalls) != 0 {
		t.Fatalf("offline replay called live adapters: service=%#v state=%#v", engine.serviceCalls, engine.stateCalls)
	}
}

func TestReceiptReplayRejectsObservationThatDisagreesWithComponents(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	value, _ := replayFixture(t, application)
	var observation cycle.Observation
	if err := application.Store.ReadJSON(value.Cycle.ID, "observation.json", &observation); err != nil {
		t.Fatal(err)
	}
	observation.Beads = nil
	updated, err := application.Store.WriteJSON(value.Cycle.ID, "observation", "observation.json", observation)
	if err != nil {
		t.Fatal(err)
	}
	value.Cycle.Artifacts = replaceReplayArtifact(t, value.Cycle.Artifacts, updated)
	rewriteReplayReceipt(t, application, value.Cycle, value.TerminalAt)

	code, _, stderr := runJSON(t, application, "receipt", "replay", value.Cycle.ID, "--json")
	if code != ExitFailure || !strings.Contains(stderr, "beads") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestReceiptReplayRejectsEvidenceIDMissingFromObservation(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	value, _ := replayFixture(t, application)
	var beadsDigest string
	for _, artifact := range value.InputArtifacts {
		if artifact.Kind == "beads" {
			beadsDigest = artifact.Digest
		}
	}
	candidate := *value.Cycle.Candidate
	candidate.Evidence = []domain.EvidenceRef{{Kind: "bead", ID: "Revel-missing", Digest: beadsDigest}}
	judgment := *value.Cycle.Judgment
	judgment.Opportunities = append([]domain.Candidate(nil), judgment.Opportunities...)
	judgment.Opportunities[*judgment.SelectedIndex] = candidate
	judgmentArtifact, err := application.Store.WriteJSON(value.Cycle.ID, "judgment", "judgment.json", judgment)
	if err != nil {
		t.Fatal(err)
	}
	value.Cycle.Judgment = &judgment
	value.Cycle.Candidate = &candidate
	value.Cycle.CandidateHash = domain.FingerprintCandidate(candidate)
	value.Cycle.Artifacts = replaceReplayArtifact(t, value.Cycle.Artifacts, judgmentArtifact)
	rewriteReplayReceipt(t, application, value.Cycle, value.TerminalAt)

	code, _, stderr := runJSON(t, application, "receipt", "replay", value.Cycle.ID, "--json")
	if code != ExitFailure || !strings.Contains(stderr, "not in the observation") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestReceiptReplayValidatesNoOpObservationAndJudgment(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	value, _ := replayFixture(t, application)
	invalidObservation, err := json.Marshal(map[string]any{"cycle_id": value.Cycle.ID})
	if err != nil {
		t.Fatal(err)
	}
	observationArtifact, err := application.Store.WriteBytes(value.Cycle.ID, "observation", "observation.json", append(invalidObservation, '\n'))
	if err != nil {
		t.Fatal(err)
	}
	judgment := domain.Judgment{SchemaVersion: domain.JudgmentSchemaV1, NoOpReason: "No bounded opportunity clears the evidence threshold."}
	judgmentArtifact, err := application.Store.WriteJSON(value.Cycle.ID, "judgment", "judgment.json", judgment)
	if err != nil {
		t.Fatal(err)
	}
	value.Cycle.Judgment = &judgment
	value.Cycle.Candidate = nil
	value.Cycle.CandidateHash = ""
	value.Cycle.ContractHash = ""
	value.Cycle.Artifacts = replaceReplayArtifact(t, value.Cycle.Artifacts, observationArtifact)
	value.Cycle.Artifacts = replaceReplayArtifact(t, value.Cycle.Artifacts, judgmentArtifact)
	rewriteReplayReceipt(t, application, value.Cycle, value.TerminalAt)

	code, _, stderr := runJSON(t, application, "receipt", "replay", value.Cycle.ID, "--json")
	if code != ExitFailure || !strings.Contains(stderr, "observation") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestReceiptReplayRejectsMutatedStoredInput(t *testing.T) {
	engine := &fakeEngine{}
	application := testApplication(t, engine)
	_, observationPath := replayFixture(t, application)
	if err := os.WriteFile(observationPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runJSON(t, application, "receipt", "replay", "cycle-replay", "--json")
	if code != ExitFailure || !strings.Contains(stderr, "digest") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestReceiptReplayRejectsArtifactPartitionOmission(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	value, _ := replayFixture(t, application)
	if len(value.InputArtifacts) < 2 {
		t.Fatalf("fixture input artifacts = %#v", value.InputArtifacts)
	}
	value.InputArtifacts = value.InputArtifacts[1:]
	data, _, err := receiptpkg.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Store.WriteBytes("cycle-replay", "receipt", "receipt.json", data); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runJSON(t, application, "receipt", "replay", "cycle-replay", "--json")
	if code != ExitFailure || !strings.Contains(stderr, "artifact partition") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestReceiptReplayAcceptsEarlyFailedReceiptWithoutObservation(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	now := time.Date(2026, 7, 13, 19, 0, 0, 0, time.UTC)
	value, err := receiptpkg.Build(domain.Cycle{
		SchemaVersion: domain.CycleSchemaV1, ID: "cycle-early-failure", RunID: "run-failed", Portfolio: "sylveste",
		Mode: domain.ModeProposal, Stage: domain.StageFailed, CreatedAt: now.Add(-time.Second), UpdatedAt: now,
		IdempotencyKeys: map[string]string{"event:failed": "recorded"}, Failure: "read canonical backlog: unavailable",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := receiptpkg.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Store.WriteBytes(value.Cycle.ID, "receipt", "receipt.json", data); err != nil {
		t.Fatal(err)
	}

	code, replayed, stderr := runJSON(t, application, "receipt", "replay", value.Cycle.ID, "--json")
	if code != 0 || stderr != "" || replayed["verified"] != true || replayed["inputs_verified"] != float64(0) || replayed["outputs_verified"] != float64(0) {
		t.Fatalf("code=%d replay=%#v stderr=%q", code, replayed, stderr)
	}
}

func TestReceiptReplayRejectsFailedReceiptWithForgedCycleNoOpReason(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	value, _ := replayFixture(t, application)
	value.Cycle.Stage = domain.StageFailed
	value.Cycle.Failure = "candidate repository is outside allowed roots"
	value.Cycle.Candidate = nil
	value.Cycle.CandidateHash = ""
	value.Cycle.ContractHash = ""
	value.Cycle.NoOpReason = "forged resolution"
	value.Cycle.RoadmapDigest = ""
	kept := value.Cycle.Artifacts[:0]
	for _, artifact := range value.Cycle.Artifacts {
		if artifact.Kind != "roadmap-output" {
			kept = append(kept, artifact)
		}
	}
	value.Cycle.Artifacts = kept
	rewriteReplayReceipt(t, application, value.Cycle, value.TerminalAt)

	code, _, stderr := runJSON(t, application, "receipt", "replay", value.Cycle.ID, "--json")
	if code != ExitFailure || !strings.Contains(stderr, "no-op") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestReceiptReplayAcceptsFailedRejectedJudgmentWithoutCandidate(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	value, _ := replayFixture(t, application)
	value.Cycle.Stage = domain.StageFailed
	value.Cycle.Failure = "candidate repository is outside allowed roots"
	value.Cycle.Candidate = nil
	value.Cycle.CandidateHash = ""
	value.Cycle.ContractHash = ""
	value.Cycle.RoadmapDigest = ""
	kept := value.Cycle.Artifacts[:0]
	for _, artifact := range value.Cycle.Artifacts {
		if artifact.Kind != "roadmap-output" {
			kept = append(kept, artifact)
		}
	}
	value.Cycle.Artifacts = kept
	rewriteReplayReceipt(t, application, value.Cycle, value.TerminalAt)

	code, replayed, stderr := runJSON(t, application, "receipt", "replay", value.Cycle.ID, "--json")
	if code != 0 || stderr != "" || replayed["verified"] != true || replayed["contract_hash"] != nil {
		t.Fatalf("code=%d replay=%#v stderr=%q", code, replayed, stderr)
	}
}

func TestReceiptReplayRejectsRoadmapDigestNotBoundToOutput(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	value, _ := replayFixture(t, application)
	value.Cycle.RoadmapDigest = strings.Repeat("f", 64)
	rewriteReplayReceipt(t, application, value.Cycle, value.TerminalAt)

	code, _, stderr := runJSON(t, application, "receipt", "replay", value.Cycle.ID, "--json")
	if code != ExitFailure || !strings.Contains(stderr, "roadmap") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestReceiptReplayRejectsArbitrarySelectedCandidateNoOpReason(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	value, _ := replayFixture(t, application)
	value.Cycle.NoOpReason = "forged terminal resolution"
	rewriteReplayReceipt(t, application, value.Cycle, value.TerminalAt)

	code, _, stderr := runJSON(t, application, "receipt", "replay", value.Cycle.ID, "--json")
	if code != ExitFailure || !strings.Contains(stderr, "no-op") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestReceiptCommandsRejectUnsafeCycleID(t *testing.T) {
	application := testApplication(t, &fakeEngine{})
	code, _, stderr := runJSON(t, application, "receipt", "show", filepath.Join("..", "escape"), "--json")
	if code != ExitFailure || !strings.Contains(stderr, "unsafe") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}
