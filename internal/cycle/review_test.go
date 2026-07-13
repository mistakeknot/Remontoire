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

	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

type dynamicReviewer struct {
	calls int
	call  func(harness.ReviewRequest) domain.Review
}

func (r *dynamicReviewer) Review(_ context.Context, request harness.ReviewRequest) (domain.Review, harness.Metadata, error) {
	r.calls++
	return r.call(request), harness.Metadata{Backend: "claude", Model: "review-fixture", Turns: 2, CostUSD: 0.1}, nil
}

type fakeRoadmap struct {
	calls  int
	digest string
	err    error
}

func (r *fakeRoadmap) Sync(context.Context) (string, error) {
	r.calls++
	return r.digest, r.err
}

func reviewerFor(t *testing.T, verdict domain.Verdict) *dynamicReviewer {
	t.Helper()
	return &dynamicReviewer{call: func(request harness.ReviewRequest) domain.Review {
		var material ReviewMaterial
		if err := json.Unmarshal(request.Material, &material); err != nil {
			t.Fatalf("decode review material: %v", err)
		}
		artifacts := map[string]domain.Artifact{}
		for _, artifact := range material.Artifacts {
			artifacts[artifact.Kind] = artifact
		}
		evidence := make([]domain.EvidenceRef, 0, 4)
		for _, kind := range []string{"patch", "execution-report", "executor-transcript", "measurement"} {
			artifact, ok := artifacts[kind]
			if !ok {
				t.Fatalf("review material has no %s artifact", kind)
			}
			evidence = append(evidence, domain.EvidenceRef{Kind: kind, ID: filepath.Base(artifact.Path), Digest: artifact.Digest})
		}
		return domain.Review{
			SchemaVersion: domain.ReviewSchemaV1,
			ContractHash:  request.ContractHash,
			Verdict:       verdict,
			Rationale:     "The measured result and bounded patch support this verdict.",
			Evidence:      evidence,
		}
	}}
}

func reviewedService(t *testing.T, verdict domain.Verdict) (*Service, *fakeKernel, *fakeBacklog, *dynamicReviewer, *fakeRoadmap) {
	t.Helper()
	service, kernel, backlog, _, _, _ := executionService(t)
	reviewer := reviewerFor(t, verdict)
	roadmapPath := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(roadmapPath, []byte("# Canonical roadmap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roadmapArtifact, err := service.Store.HashExisting("roadmap-output", roadmapPath)
	if err != nil {
		t.Fatal(err)
	}
	roadmap := &fakeRoadmap{digest: roadmapArtifact.Digest}
	service.Reviewers = map[string]Reviewer{"claude": reviewer}
	service.Config.ReviewerBackend = "claude"
	service.Config.ReviewSchemaPath = filepath.Join(t.TempDir(), "review.json")
	service.Config.RoadmapPath = roadmapPath
	service.Roadmap = roadmap
	return service, kernel, backlog, reviewer, roadmap
}

func executeToReview(t *testing.T, service *Service) domain.Cycle {
	t.Helper()
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "mk"); err != nil {
		t.Fatal(err)
	}
	cycle, err = service.Execute(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	return cycle
}

func TestReviewAndCompoundPromotionClosesLoopExactlyOnce(t *testing.T) {
	service, kernel, backlog, reviewer, roadmap := reviewedService(t, domain.VerdictPromote)
	cycle := executeToReview(t, service)
	reviewed, err := service.Review(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.Stage != domain.StageCompounding || reviewed.Resolution == nil || reviewed.Resolution.FinalVerdict != domain.VerdictPromote {
		t.Fatalf("reviewed cycle = %#v", reviewed)
	}
	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Stage != domain.StageCompleted || completed.PromotionBeadID == "" || completed.SignedReceiptID == "" || completed.RoadmapDigest == "" {
		t.Fatalf("completed cycle = %#v", completed)
	}
	if kernel.verifyCalls != 1 {
		t.Fatalf("receipt verification calls = %d", kernel.verifyCalls)
	}
	if backlog.promotionCalls != 1 || backlog.closeCalls != 1 || reviewer.calls != 1 || roadmap.calls != 1 || kernel.receiptCalls != 1 {
		t.Fatalf("calls promotion=%d close=%d review=%d roadmap=%d receipt=%d", backlog.promotionCalls, backlog.closeCalls, reviewer.calls, roadmap.calls, kernel.receiptCalls)
	}
	if !strings.Contains(backlog.promotionEvidence, "Verdict: promote") ||
		!strings.Contains(backlog.promotionEvidence, "Contract: "+cycle.ContractHash) ||
		!strings.Contains(backlog.promotionEvidence, "patch: ") ||
		!strings.Contains(backlog.promotionEvidence, "measurement: ") {
		t.Fatalf("promotion evidence = %q", backlog.promotionEvidence)
	}
	if kernel.advanceCalls != 5 {
		t.Fatalf("run advanced %d times; compound is already terminal", kernel.advanceCalls)
	}
	if len(kernel.outcomes) != 1 || kernel.outcomes[cycle.ID].FinalVerdict != domain.VerdictPromote {
		t.Fatalf("outcomes = %#v", kernel.outcomes)
	}
	again, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.SignedReceiptID != completed.SignedReceiptID || backlog.promotionCalls != 1 || backlog.closeCalls != 1 || roadmap.calls != 1 || kernel.receiptCalls != 1 {
		t.Fatal("repeated compound duplicated a side effect")
	}
	if _, err := os.Stat(filepath.Join(service.Config.ArtifactRoot, "cycles", cycle.ID, "receipt.json")); err != nil {
		t.Fatalf("receipt artifact: %v", err)
	}
	var signed domain.Receipt
	if err := service.Store.ReadJSON(cycle.ID, "receipt.json", &signed); err != nil {
		t.Fatal(err)
	}
	if signed.Cycle.RoadmapDigest != completed.RoadmapDigest || signed.Cycle.Stage != domain.StageCompleted {
		t.Fatalf("receipt terminal projection = %#v", signed.Cycle)
	}
}

func TestMeasuredFailureVetoesReviewerPromotion(t *testing.T) {
	service, kernel, backlog, _, _ := reviewedService(t, domain.VerdictPromote)
	cycle := executeToReview(t, service)
	cycle.Measurement.Value = cycle.Candidate.Contract.Metric.Target + 10
	measurementArtifact, err := service.Store.WriteJSON(cycle.ID, "measurement", "measurement.json", *cycle.Measurement)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cycle.Artifacts {
		if cycle.Artifacts[i].Kind == "measurement" {
			cycle.Artifacts[i] = measurementArtifact
		}
	}
	kernel.cycles[cycle.ID] = cycle
	reviewed, err := service.Review(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.Resolution.FinalVerdict != domain.VerdictCloseFailure || !strings.Contains(reviewed.Resolution.OverrideReason, "target") {
		t.Fatalf("resolution = %#v", reviewed.Resolution)
	}
	if _, err := service.Compound(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	if backlog.promotionCalls != 0 || backlog.closeCalls != 1 {
		t.Fatalf("promotion=%d close=%d", backlog.promotionCalls, backlog.closeCalls)
	}
}

func TestInconclusiveReviewClosesExperimentWithoutPromotion(t *testing.T) {
	service, kernel, backlog, _, _ := reviewedService(t, domain.VerdictInconclusive)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if backlog.promotionCalls != 0 || backlog.closeCalls != 1 {
		t.Fatalf("promotion=%d close=%d", backlog.promotionCalls, backlog.closeCalls)
	}
	if kernel.outcomes[cycle.ID].FinalVerdict != domain.VerdictInconclusive || completed.PromotionBeadID != "" {
		t.Fatalf("outcome=%#v completed=%#v", kernel.outcomes[cycle.ID], completed)
	}
}

func TestDiscoveryEvidenceReceivesOutcomeFeedback(t *testing.T) {
	service, kernel, _, _, _ := reviewedService(t, domain.VerdictCloseSuccess)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		var observation Observation
		if err := json.Unmarshal(request.Observation, &observation); err != nil {
			t.Fatal(err)
		}
		for _, artifact := range observation.Artifacts {
			if artifact.Kind == "discoveries" {
				judgment.Opportunities[0].Evidence = []domain.EvidenceRef{{Kind: "discovery", ID: "disc-1", Digest: artifact.Digest}}
			}
		}
		return judgment
	}}
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Compound(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	if kernel.feedbackCalls != 1 {
		t.Fatalf("feedback calls = %d", kernel.feedbackCalls)
	}
	if kernel.feedbackWrites != 1 {
		t.Fatalf("feedback writes = %d", kernel.feedbackWrites)
	}
}

func TestDiscoveryFeedbackRetryResolvesCommitThenTimeoutWithoutDuplicate(t *testing.T) {
	service, kernel, _, _, _ := reviewedService(t, domain.VerdictCloseSuccess)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		var observation Observation
		if err := json.Unmarshal(request.Observation, &observation); err != nil {
			t.Fatal(err)
		}
		for _, artifact := range observation.Artifacts {
			if artifact.Kind == "discoveries" {
				judgment.Opportunities[0].Evidence = []domain.EvidenceRef{{Kind: "discovery", ID: "disc-1", Digest: artifact.Digest}}
			}
		}
		return judgment
	}}
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	kernel.feedbackErr = errors.New("feedback response timed out")
	kernel.feedbackStoreErr = true

	interrupted, err := service.Compound(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("cycle=%#v error=%v", interrupted, err)
	}
	if interrupted.IdempotencyKeys["feedback:disc-1"] != "started" || kernel.feedbackCalls != 1 || kernel.feedbackWrites != 1 {
		t.Fatalf("keys=%#v calls=%d writes=%d", interrupted.IdempotencyKeys, kernel.feedbackCalls, kernel.feedbackWrites)
	}

	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Stage != domain.StageCompleted || kernel.feedbackCalls != 2 || kernel.feedbackWrites != 1 {
		t.Fatalf("completed=%#v calls=%d writes=%d", completed, kernel.feedbackCalls, kernel.feedbackWrites)
	}
}

func TestRoadmapFailureLeavesCompoundingAndRetryDoesNotRepeatBacklog(t *testing.T) {
	service, _, backlog, _, roadmap := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	roadmap.err = errors.New("roadmap unavailable")
	failed, err := service.Compound(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "roadmap") || failed.Stage != domain.StageCompounding {
		t.Fatalf("cycle=%#v error=%v", failed, err)
	}
	if backlog.closeCalls != 1 {
		t.Fatalf("close calls = %d", backlog.closeCalls)
	}
	roadmap.err = nil
	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Stage != domain.StageCompleted || backlog.closeCalls != 1 || roadmap.calls != 2 {
		t.Fatalf("completed=%#v close=%d roadmap=%d", completed, backlog.closeCalls, roadmap.calls)
	}
}

func TestRoadmapDigestMustMatchGeneratedArtifactBeforeReceipt(t *testing.T) {
	service, kernel, backlog, _, roadmap := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	roadmapPath := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(roadmapPath, []byte("# Canonical roadmap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service.Config.RoadmapPath = roadmapPath
	actual, err := service.Store.HashExisting("roadmap-output", roadmapPath)
	if err != nil {
		t.Fatal(err)
	}
	roadmap.digest = strings.Repeat("f", 64)

	failed, err := service.Compound(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "roadmap digest") || failed.Stage != domain.StageCompounding {
		t.Fatalf("cycle=%#v error=%v", failed, err)
	}
	if kernel.receiptCalls != 0 || failed.IdempotencyKeys["roadmap:sync"] != "started" {
		t.Fatalf("receipt=%d keys=%#v", kernel.receiptCalls, failed.IdempotencyKeys)
	}

	roadmap.digest = actual.Digest
	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Stage != domain.StageCompleted || completed.RoadmapDigest != actual.Digest || roadmap.calls != 2 || backlog.closeCalls != 1 {
		t.Fatalf("completed=%#v roadmap=%d close=%d", completed, roadmap.calls, backlog.closeCalls)
	}
}

func TestCompoundRequiresAbsoluteRoadmapPathBeforeBacklogMutation(t *testing.T) {
	service, kernel, backlog, _, roadmap := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	service.Config.RoadmapPath = ""

	_, err := service.Compound(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "absolute roadmap") {
		t.Fatalf("error = %v", err)
	}
	if backlog.closeCalls != 0 || len(kernel.outcomes) != 0 || roadmap.calls != 0 || kernel.receiptCalls != 0 {
		t.Fatalf("close=%d outcomes=%d roadmap=%d receipts=%d", backlog.closeCalls, len(kernel.outcomes), roadmap.calls, kernel.receiptCalls)
	}
}

func TestCompoundRequiresRoadmapAdapterBeforeBacklogMutation(t *testing.T) {
	service, kernel, backlog, _, roadmap := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	service.Roadmap = nil

	_, err := service.Compound(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "roadmap adapter") {
		t.Fatalf("error = %v", err)
	}
	if backlog.closeCalls != 0 || len(kernel.outcomes) != 0 || roadmap.calls != 0 || kernel.receiptCalls != 0 {
		t.Fatalf("close=%d outcomes=%d roadmap=%d receipts=%d", backlog.closeCalls, len(kernel.outcomes), roadmap.calls, kernel.receiptCalls)
	}
}

func TestRoadmapDigestMustBeCanonicalLowercaseSHA256(t *testing.T) {
	service, kernel, _, _, roadmap := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	roadmap.digest = strings.Repeat("A", 64)

	_, err := service.Compound(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "lowercase SHA-256") {
		t.Fatalf("error = %v", err)
	}
	if kernel.receiptCalls != 0 {
		t.Fatalf("receipt calls = %d", kernel.receiptCalls)
	}
}

func TestRoadmapSnapshotSurvivesExternalRegenerationBeforeReceiptRecovery(t *testing.T) {
	service, kernel, _, _, roadmap := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	kernel.emitErr = errors.New("receipt service unavailable")

	interrupted, err := service.Compound(context.Background(), cycle.ID)
	if err == nil || interrupted.IdempotencyKeys["roadmap:sync"] != "completed" || roadmap.calls != 1 || kernel.receiptCalls != 1 {
		t.Fatalf("cycle=%#v error=%v roadmap=%d receipts=%d", interrupted, err, roadmap.calls, kernel.receiptCalls)
	}
	if err := os.WriteFile(service.Config.RoadmapPath, []byte("# Mutated roadmap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kernel.emitErr = nil

	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil || completed.Stage != domain.StageCompleted || completed.SignedReceiptID == "" || kernel.receiptCalls != 2 {
		t.Fatalf("completed=%#v error=%v receipt calls=%d", completed, err, kernel.receiptCalls)
	}
}

func TestRoadmapSnapshotIsReverifiedBeforeReceiptRecovery(t *testing.T) {
	service, kernel, _, _, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	kernel.emitErr = errors.New("receipt service unavailable")
	interrupted, err := service.Compound(context.Background(), cycle.ID)
	if err == nil {
		t.Fatal("expected receipt interruption")
	}
	var snapshot domain.Artifact
	for _, artifact := range interrupted.Artifacts {
		if artifact.Kind == "roadmap-output" {
			snapshot = artifact
		}
	}
	if snapshot.Path == "" || snapshot.Path == service.Config.RoadmapPath {
		t.Fatalf("roadmap snapshot = %#v", snapshot)
	}
	if err := os.WriteFile(snapshot.Path, []byte("# Mutated snapshot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kernel.emitErr = nil

	_, err = service.Compound(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "roadmap") || kernel.receiptCalls != 1 {
		t.Fatalf("error=%v receipt calls=%d", err, kernel.receiptCalls)
	}
}

func TestStartedReviewWithoutOutputNeverReinvokesReviewer(t *testing.T) {
	service, kernel, _, reviewer, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	cycle.IdempotencyKeys["review:attempt"] = "started"
	kernel.cycles[cycle.ID] = cycle
	_, err := service.Review(context.Background(), cycle.ID)
	if !errors.Is(err, ErrReviewIndeterminate) {
		t.Fatalf("error = %v", err)
	}
	if reviewer.calls != 0 {
		t.Fatal("indeterminate review was invoked twice")
	}
}

func TestStartedRunAdvanceRecoversFromCanonicalPhase(t *testing.T) {
	service, kernel, _, reviewer, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	reviewed, err := service.Review(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	reviewed.Stage = domain.StageReviewing
	reviewed.IdempotencyKeys["run:compound"] = "started"
	kernel.cycles[cycle.ID] = reviewed
	beforeAdvance := kernel.advanceCalls
	recovered, err := service.Review(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Stage != domain.StageCompounding || kernel.advanceCalls != beforeAdvance || reviewer.calls != 1 {
		t.Fatalf("recovered=%#v advances=%d reviewer=%d", recovered, kernel.advanceCalls, reviewer.calls)
	}
}

func TestReviewRetryRepairsStageEventAfterStateFirstFailure(t *testing.T) {
	service, kernel, _, reviewer, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	kernel.eventErrors[domain.StageCompounding] = errors.New("event store unavailable")
	failed, err := service.Review(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "record compounding event") {
		t.Fatalf("cycle=%#v error=%v", failed, err)
	}
	persisted := kernel.cycles[cycle.ID]
	if persisted.Stage != domain.StageCompounding || persisted.IdempotencyKeys["event:compounding"] != "" {
		t.Fatalf("persisted = %#v", persisted)
	}

	delete(kernel.eventErrors, domain.StageCompounding)
	recovered, err := service.Review(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Stage != domain.StageCompounding || recovered.IdempotencyKeys["event:compounding"] != "recorded" {
		t.Fatalf("recovered = %#v", recovered)
	}
	if reviewer.calls != 1 || stageCount(kernel.events, domain.StageCompounding) != 1 {
		t.Fatalf("reviewer=%d events=%#v", reviewer.calls, kernel.events)
	}
}

func TestCompoundRetryRepairsCompletedEventWithoutRepeatingSideEffects(t *testing.T) {
	service, kernel, backlog, _, roadmap := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	kernel.eventErrors[domain.StageCompleted] = errors.New("event store unavailable")
	failed, err := service.Compound(context.Background(), cycle.ID)
	if err == nil || failed.Stage != domain.StageCompleted || failed.SignedReceiptID == "" {
		t.Fatalf("cycle=%#v error=%v", failed, err)
	}

	delete(kernel.eventErrors, domain.StageCompleted)
	recovered, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.IdempotencyKeys["event:completed"] != "recorded" || stageCount(kernel.events, domain.StageCompleted) != 1 {
		t.Fatalf("recovered=%#v events=%#v", recovered, kernel.events)
	}
	if kernel.receiptCalls != 1 || backlog.closeCalls != 1 || roadmap.calls != 1 {
		t.Fatalf("receipt=%d close=%d roadmap=%d", kernel.receiptCalls, backlog.closeCalls, roadmap.calls)
	}
}

func TestOutcomeAppearsInNextObservation(t *testing.T) {
	service, kernel, _, _, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Compound(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	service.NewID = func(time.Time) (string, error) { return "cycle-2", nil }
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		var observation Observation
		if err := json.Unmarshal(request.Observation, &observation); err != nil {
			t.Fatal(err)
		}
		if len(observation.PriorOutcomes) != 1 || observation.PriorOutcomes[0].CycleID != cycle.ID {
			t.Fatalf("prior outcomes = %#v", observation.PriorOutcomes)
		}
		return domain.Judgment{SchemaVersion: domain.JudgmentSchemaV1, NoOpReason: "Outcome already resolves the uncertainty."}
	}}
	if _, err := service.Start(context.Background(), domain.ModeShadow); err != nil {
		t.Fatal(err)
	}
	if len(kernel.outcomes) != 1 {
		t.Fatalf("outcomes changed unexpectedly: %#v", kernel.outcomes)
	}
}

func TestReviewEvidenceMustBindToCycleArtifact(t *testing.T) {
	service, _, _, reviewer, _ := reviewedService(t, domain.VerdictCloseSuccess)
	reviewer.call = func(request harness.ReviewRequest) domain.Review {
		return domain.Review{SchemaVersion: domain.ReviewSchemaV1, ContractHash: request.ContractHash, Verdict: domain.VerdictCloseSuccess, Rationale: "forged", Evidence: []domain.EvidenceRef{{Kind: "measurement", ID: "measurement.json", Digest: strings.Repeat("f", 64)}}}
	}
	cycle := executeToReview(t, service)
	_, err := service.Review(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("error = %v", err)
	}
}

func TestPromotionReviewRequiresCompleteExecutionEvidence(t *testing.T) {
	service, _, _, reviewer, _ := reviewedService(t, domain.VerdictPromote)
	completeReview := reviewer.call
	reviewer.call = func(request harness.ReviewRequest) domain.Review {
		review := completeReview(request)
		filtered := review.Evidence[:0]
		for _, ref := range review.Evidence {
			if ref.Kind != "patch" {
				filtered = append(filtered, ref)
			}
		}
		review.Evidence = filtered
		return review
	}
	cycle := executeToReview(t, service)
	_, err := service.Review(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "patch") {
		t.Fatalf("error = %v", err)
	}
}

func TestReviewRejectsContractProjectionTamperingBeforeReviewer(t *testing.T) {
	service, kernel, _, reviewer, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	cycle.Candidate.Contract.PromotionCriteria = "changed after approval"
	kernel.cycles[cycle.ID] = cycle

	_, err := service.Review(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "contract") {
		t.Fatalf("error = %v", err)
	}
	if reviewer.calls != 0 {
		t.Fatalf("reviewer calls = %d", reviewer.calls)
	}
}

func TestReviewRejectsCanonicalMeasurementDifferentFromArtifact(t *testing.T) {
	service, kernel, _, reviewer, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	cycle.Measurement.Value++
	kernel.cycles[cycle.ID] = cycle

	_, err := service.Review(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "measurement") {
		t.Fatalf("error = %v", err)
	}
	if reviewer.calls != 0 {
		t.Fatalf("reviewer calls = %d", reviewer.calls)
	}
}

func TestReviewRejectsArtifactChangedAfterMeasurement(t *testing.T) {
	service, _, _, reviewer, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	measurementPath, err := service.Store.Path(cycle.ID, "measurement.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(measurementPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = service.Review(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("error = %v", err)
	}
	if reviewer.calls != 0 {
		t.Fatal("reviewer saw mutated evidence")
	}
}

func TestCompoundRevalidatesReviewedEvidenceBeforeBacklogMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Service, *domain.Cycle)
		want   string
	}{
		{
			name: "candidate",
			mutate: func(_ *testing.T, _ *Service, cycle *domain.Cycle) {
				cycle.Candidate.Title += " changed"
			},
			want: "candidate",
		},
		{
			name: "contract",
			mutate: func(_ *testing.T, _ *Service, cycle *domain.Cycle) {
				cycle.Candidate.Contract.PromotionCriteria += " changed"
			},
			want: "contract",
		},
		{
			name: "approval",
			mutate: func(_ *testing.T, _ *Service, cycle *domain.Cycle) {
				cycle.Approval.ContractHash = strings.Repeat("f", 64)
			},
			want: "approval",
		},
		{
			name: "measurement",
			mutate: func(_ *testing.T, _ *Service, cycle *domain.Cycle) {
				cycle.Measurement.Value++
			},
			want: "measurement",
		},
		{
			name: "review evidence",
			mutate: func(_ *testing.T, _ *Service, cycle *domain.Cycle) {
				cycle.Review.Evidence[0].Digest = strings.Repeat("f", 64)
			},
			want: "evidence",
		},
		{
			name: "artifact contents",
			mutate: func(t *testing.T, _ *Service, cycle *domain.Cycle) {
				t.Helper()
				for _, artifact := range cycle.Artifacts {
					if artifact.Kind == "patch" {
						if err := os.WriteFile(artifact.Path, []byte("mutated patch\n"), 0o600); err != nil {
							t.Fatal(err)
						}
						return
					}
				}
				t.Fatal("patch artifact not found")
			},
			want: "changed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, kernel, backlog, _, roadmap := reviewedService(t, domain.VerdictPromote)
			cycle := executeToReview(t, service)
			reviewed, err := service.Review(context.Background(), cycle.ID)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, service, &reviewed)
			kernel.cycles[cycle.ID] = reviewed

			_, err = service.Compound(context.Background(), cycle.ID)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if backlog.promotionCalls != 0 || backlog.closeCalls != 0 || len(kernel.outcomes) != 0 || roadmap.calls != 0 || kernel.receiptCalls != 0 {
				t.Fatalf("promotion=%d close=%d outcomes=%d roadmap=%d receipts=%d", backlog.promotionCalls, backlog.closeCalls, len(kernel.outcomes), roadmap.calls, kernel.receiptCalls)
			}
		})
	}
}

func TestPromotionEvidenceUsesOnlyReviewValidatedDigests(t *testing.T) {
	service, kernel, backlog, _, _ := reviewedService(t, domain.VerdictPromote)
	cycle := executeToReview(t, service)
	reviewed, err := service.Review(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	var reviewedPatch string
	for _, ref := range reviewed.Review.Evidence {
		if ref.Kind == "patch" {
			reviewedPatch = ref.Digest
		}
	}
	if reviewedPatch == "" {
		t.Fatal("review has no patch evidence")
	}
	forged := strings.Repeat("f", 64)
	reviewed.Artifacts = append([]domain.Artifact{{Kind: "patch", Path: "/not/reviewed.patch", Digest: forged}}, reviewed.Artifacts...)
	kernel.cycles[cycle.ID] = reviewed

	if _, err := service.Compound(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(backlog.promotionEvidence, "patch: "+reviewedPatch) || strings.Contains(backlog.promotionEvidence, "patch: "+forged) {
		t.Fatalf("promotion evidence = %q", backlog.promotionEvidence)
	}
}

func TestReceiptRecoveryRejectsMismatchedLocalSignature(t *testing.T) {
	service, kernel, _, _, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed.Stage = domain.StageCompounding
	completed.SignedReceiptID = ""
	completed.IdempotencyKeys["receipt:attempt"] = "started"
	kernel.cycles[cycle.ID] = completed
	if _, err := service.Store.WriteJSON(cycle.ID, "receipt-signature", "receipt-signature.json", receiptSignature{
		ReceiptID: "receipt-forged", ContentHash: strings.Repeat("f", 64),
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Compound(context.Background(), cycle.ID)
	if !errors.Is(err, ErrReceiptIndeterminate) {
		t.Fatalf("error = %v", err)
	}
}

func TestReceiptRecoveryRejectsUnrelatedValidReceiptID(t *testing.T) {
	service, kernel, _, _, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed.Stage = domain.StageCompounding
	completed.SignedReceiptID = ""
	completed.IdempotencyKeys["receipt:attempt"] = "started"
	kernel.cycles[cycle.ID] = completed
	if _, err := service.Store.WriteJSON(cycle.ID, "receipt-signature", "receipt-signature.json", receiptSignature{
		ReceiptID: "receipt-valid-but-unrelated", ContentHash: completed.IdempotencyKeys["receipt:content"],
	}); err != nil {
		t.Fatal(err)
	}

	_, err = service.Compound(context.Background(), cycle.ID)
	if !errors.Is(err, ErrReceiptIndeterminate) || !strings.Contains(err.Error(), "exact receipt") {
		t.Fatalf("error = %v", err)
	}
}

func TestReceiptRecoveryUsesVerifiedSignatureWithoutSecondEmit(t *testing.T) {
	service, kernel, _, _, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed.Stage = domain.StageCompounding
	completed.SignedReceiptID = ""
	completed.IdempotencyKeys["receipt:attempt"] = "started"
	kernel.cycles[cycle.ID] = completed
	recovered, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Stage != domain.StageCompleted || recovered.SignedReceiptID == "" {
		t.Fatalf("recovered = %#v", recovered)
	}
	if kernel.receiptCalls != 1 || kernel.verifyCalls != 2 {
		t.Fatalf("emit=%d verify=%d", kernel.receiptCalls, kernel.verifyCalls)
	}
}

func TestReceiptEmitFailureCanResumeWithoutPermanentWedge(t *testing.T) {
	service, kernel, _, _, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	kernel.emitErr = errors.New("receipt service unavailable")
	failed, err := service.Compound(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "emit signed receipt") {
		t.Fatalf("cycle=%#v error=%v", failed, err)
	}
	if failed.IdempotencyKeys["receipt:attempt"] != "started" {
		t.Fatalf("receipt attempt = %q", failed.IdempotencyKeys["receipt:attempt"])
	}

	kernel.emitErr = nil
	recovered, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Stage != domain.StageCompleted || recovered.SignedReceiptID == "" {
		t.Fatalf("recovered = %#v", recovered)
	}
	if kernel.receiptCalls != 2 || kernel.findCalls == 0 {
		t.Fatalf("emit calls=%d find calls=%d", kernel.receiptCalls, kernel.findCalls)
	}
}

func TestReceiptEmitTimeoutRecoversInsertedReceiptWithoutSecondEmit(t *testing.T) {
	service, kernel, _, _, _ := reviewedService(t, domain.VerdictCloseSuccess)
	cycle := executeToReview(t, service)
	if _, err := service.Review(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	kernel.emitErr = errors.New("response timed out")
	kernel.emitStoresOnError = true

	completed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Stage != domain.StageCompleted || completed.SignedReceiptID != "receipt-1" {
		t.Fatalf("completed = %#v", completed)
	}
	if kernel.receiptCalls != 1 || kernel.findCalls != 2 || kernel.verifyCalls != 1 {
		t.Fatalf("emit=%d find=%d verify=%d", kernel.receiptCalls, kernel.findCalls, kernel.verifyCalls)
	}
}

func TestCompletedNoOpCanBeSignedExactlyOnce(t *testing.T) {
	service, kernel, _ := testService(t, domain.ModeShadow)
	service.Judge = dynamicJudge{call: func(harness.JudgmentRequest) domain.Judgment {
		return domain.Judgment{SchemaVersion: domain.JudgmentSchemaV1, NoOpReason: "No bounded evidence-backed opportunity."}
	}}
	cycle, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Stage != domain.StageCompleted || signed.SignedReceiptID == "" || kernel.receiptCalls != 1 {
		t.Fatalf("signed cycle=%#v calls=%d", signed, kernel.receiptCalls)
	}
	if kernel.phase != "compound" || kernel.runStatus != "completed" {
		t.Fatalf("run phase=%s status=%s", kernel.phase, kernel.runStatus)
	}
	if _, err := service.Compound(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	if kernel.receiptCalls != 1 {
		t.Fatalf("receipt emitted %d times", kernel.receiptCalls)
	}
}

func TestFailedCycleCanBeSignedWithoutCompoundingMutations(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	backlog.listErr = errors.New("canonical backlog unavailable")
	failed, startErr := service.Start(context.Background(), domain.ModeProposal)
	if startErr == nil || failed.Stage != domain.StageFailed || failed.Failure == "" {
		t.Fatalf("cycle=%#v error=%v", failed, startErr)
	}

	signed, err := service.Compound(context.Background(), failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Stage != domain.StageFailed || signed.SignedReceiptID == "" {
		t.Fatalf("signed = %#v", signed)
	}
	if kernel.phase != "compound" || kernel.runStatus != "completed" {
		t.Fatalf("run phase=%s status=%s", kernel.phase, kernel.runStatus)
	}
	if backlog.promotionCalls != 0 || backlog.closeCalls != 0 || backlog.noteCalls != 0 || len(kernel.outcomes) != 0 {
		t.Fatalf("backlog=%#v outcomes=%#v", backlog, kernel.outcomes)
	}
	var receipt domain.Receipt
	if err := service.Store.ReadJSON(failed.ID, "receipt.json", &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Cycle.Stage != domain.StageFailed || receipt.Cycle.Failure != failed.Failure {
		t.Fatalf("receipt cycle = %#v", receipt.Cycle)
	}
}

func TestInterruptedNoOpCanCompleteAndSignOnCompound(t *testing.T) {
	service, kernel, _ := testService(t, domain.ModeShadow)
	service.Judge = dynamicJudge{call: func(harness.JudgmentRequest) domain.Judgment {
		return domain.Judgment{SchemaVersion: domain.JudgmentSchemaV1, NoOpReason: "No bounded opportunity."}
	}}
	cycle, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil {
		t.Fatal(err)
	}
	cycle.Stage = domain.StageNoOp
	cycle.SignedReceiptID = ""
	delete(cycle.IdempotencyKeys, "event:completed")
	kernel.cycles[cycle.ID] = cycle

	signed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Stage != domain.StageCompleted || signed.SignedReceiptID == "" || kernel.receiptCalls != 1 {
		t.Fatalf("signed=%#v receipt calls=%d", signed, kernel.receiptCalls)
	}
}

func TestReviewFailureCanBeSignedWithoutCompoundingMutations(t *testing.T) {
	service, kernel, backlog, reviewer, roadmap := reviewedService(t, domain.VerdictCloseSuccess)
	reviewer.call = func(request harness.ReviewRequest) domain.Review {
		return domain.Review{
			SchemaVersion: domain.ReviewSchemaV1, ContractHash: request.ContractHash,
			Verdict: "unsupported", Rationale: "invalid fixture",
		}
	}
	cycle := executeToReview(t, service)
	failed, reviewErr := service.Review(context.Background(), cycle.ID)
	if reviewErr == nil || failed.Stage != domain.StageFailed {
		t.Fatalf("cycle=%#v error=%v", failed, reviewErr)
	}
	signed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Stage != domain.StageFailed || signed.SignedReceiptID == "" || kernel.receiptCalls != 1 {
		t.Fatalf("signed=%#v receipt calls=%d", signed, kernel.receiptCalls)
	}
	if kernel.phase != "compound" || kernel.runStatus != "completed" {
		t.Fatalf("run phase=%s status=%s", kernel.phase, kernel.runStatus)
	}
	if backlog.closeCalls != 0 || backlog.promotionCalls != 0 || roadmap.calls != 0 || len(kernel.outcomes) != 0 {
		t.Fatalf("backlog=%#v roadmap=%d outcomes=%#v", backlog, roadmap.calls, kernel.outcomes)
	}
}

func ExampleReviewMaterial() {
	data, _ := json.Marshal(ReviewMaterial{SchemaVersion: ReviewMaterialSchemaV1, CycleID: "cycle-1"})
	fmt.Println(strings.Contains(string(data), "remontoire.review-material/v1"))
	// Output: true
}

func stageCount(stages []domain.Stage, want domain.Stage) int {
	count := 0
	for _, stage := range stages {
		if stage == want {
			count++
		}
	}
	return count
}
