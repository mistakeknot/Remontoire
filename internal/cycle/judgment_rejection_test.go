package cycle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

// The 8aab9f13 shape used "shadow-work" for two contracts under a single
// configured parent and had one invalid minimizing target.
func TestRelativeRepositoryMapsToOneExactRoot(t *testing.T) {
	service, _, _ := testService(t, domain.ModeShadow)
	repository := service.Config.ProjectDir
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(filepath.Dir(repository), ".git"), 0o700); err != nil {
		t.Fatal(err)
	} // Nested repositories remain discoverable.
	service.Config.AllowedRepositoryRoots = []string{filepath.Dir(repository)}
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, repository)
		first := judgment.Opportunities[0]
		first.Contract.Repository = filepath.Base(repository)
		second := first
		second.Title = "Assess one crisis scenario against existing cascade evidence"
		invalid := judgment.Opportunities[0]
		invalid.Contract.Metric.Baseline = 3397.985
		invalid.Contract.Metric.Target = 60000
		judgment.Opportunities = []domain.Candidate{first, second, invalid}
		return judgment
	}}
	cycle, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil || cycle.Stage != domain.StageCompleted || len(cycle.Judgment.Opportunities) != 2 || cycle.JudgmentRetry != nil || len(cycle.JudgmentRejections) != 1 || cycle.JudgmentRejections[0].Index != 2 {
		t.Fatalf("cycle=%#v error=%v", cycle, err)
	}
	for _, opportunity := range cycle.Judgment.Opportunities {
		if opportunity.Contract.Repository != repository {
			t.Fatalf("repository=%q", opportunity.Contract.Repository)
		}
	}
}

func TestRelativeRepositoryRejectsUnknownAndAmbiguousRoots(t *testing.T) {
	service, _, _ := testService(t, domain.ModeShadow)
	rootA, rootB := t.TempDir(), t.TempDir()
	for _, root := range []string{rootA, rootB} {
		if err := os.MkdirAll(filepath.Join(root, "repo", ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	service.Config.AllowedRepositoryRoots = []string{rootA, rootB}
	repositories, err := service.configuredRepositories()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"repo", "unknown", "repo/subdir", "../repo"} {
		if _, err := service.normalizeRepository(name, repositories); err == nil {
			t.Fatalf("relative repository %q unexpectedly resolved", name)
		}
	}
	service.Config.AllowedRepositoryRoots = []string{rootA, rootA}
	if _, err := service.normalizeRepository("repo", repositories); err == nil {
		t.Fatal("duplicate configured roots unexpectedly resolved")
	}
}

func TestAllRejectedGetsOneCorrectiveRetryAndReceipt(t *testing.T) {
	service, _, _ := testService(t, domain.ModeShadow)
	calls := 0
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		calls++
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		if calls == 1 {
			judgment.Opportunities[0].Contract.Metric.Baseline = 3397.985
			judgment.Opportunities[0].Contract.Metric.Target = 60000
		} else if len(request.RejectionReasons) != 1 || !strings.Contains(request.RejectionReasons[0].Reason, "target must be below baseline") {
			t.Fatalf("retry reasons=%#v", request.RejectionReasons)
		}
		raw, err := json.Marshal(judgment)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(request.OutputPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return judgment
	}}
	cycle, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil || cycle.Stage != domain.StageCompleted || calls != 2 || cycle.JudgmentRetry == nil || cycle.JudgmentRetry.Outcome != "accepted" {
		t.Fatalf("cycle=%#v calls=%d error=%v", cycle, calls, err)
	}
	if cycle.JudgmentRetry.Attempt != 2 || cycle.JudgmentRejectionAttempt != 1 || len(cycle.JudgmentAttempts) != 2 || cycle.JudgmentAttempts[0].Artifact.Kind != "judgment-attempt-1" || cycle.JudgmentAttempts[1].Artifact.Kind != "judgment-attempt-2" {
		t.Fatalf("attempt provenance=%#v retry=%#v", cycle.JudgmentAttempts, cycle.JudgmentRetry)
	}
	for _, attempt := range cycle.JudgmentAttempts {
		if attempt.RawArtifact == nil || attempt.RawArtifact.Digest == attempt.Artifact.Digest {
			t.Fatalf("raw output was overwritten: %#v", attempt)
		}
	}
	cycle, err = service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	var receipt domain.Receipt
	if err := service.Store.ReadJSON(cycle.ID, "receipt.json", &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Cycle.JudgmentRetry == nil || receipt.Cycle.JudgmentRetry.Outcome != "accepted" {
		t.Fatalf("receipt retry=%#v", receipt.Cycle.JudgmentRetry)
	}
	if len(receipt.Cycle.JudgmentAttempts) != 2 || receipt.Cycle.JudgmentAttempts[0].RawArtifact == nil || receipt.Cycle.JudgmentAttempts[1].RawArtifact == nil {
		t.Fatalf("receipt attempts=%#v", receipt.Cycle.JudgmentAttempts)
	}
}

func TestStructurallyInvalidJudgmentDoesNotRetry(t *testing.T) {
	service, _, _ := testService(t, domain.ModeShadow)
	calls := 0
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		calls++
		return domain.Judgment{SchemaVersion: "wrong"}
	}}
	cycle, err := service.Start(context.Background(), domain.ModeShadow)
	if err == nil || cycle.Stage != domain.StageFailed || calls != 1 || cycle.JudgmentRetry != nil {
		t.Fatalf("stage=%s calls=%d retry=%#v err=%v", cycle.Stage, calls, cycle.JudgmentRetry, err)
	}
}

func TestCrashDuringCorrectiveRetryNeverReinvokesJudge(t *testing.T) {
	service, _, _ := testService(t, domain.ModeShadow)
	calls := 0
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		calls++
		if calls == 2 {
			panic("simulated crash during pooled retry")
		}
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		judgment.Opportunities[0].Contract.Metric.Target = 60000
		return judgment
	}}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected simulated crash")
			}
		}()
		_, _ = service.Start(context.Background(), domain.ModeShadow)
	}()
	cycle, err := service.ResumeObservation(context.Background(), "cycle-1")
	if err == nil || !strings.Contains(err.Error(), "indeterminate") || calls != 2 || cycle.Stage != domain.StageFailed || cycle.JudgmentRetry == nil || cycle.JudgmentRetry.Outcome != "indeterminate" {
		t.Fatalf("stage=%s calls=%d retry=%#v err=%v", cycle.Stage, calls, cycle.JudgmentRetry, err)
	}
}

func TestCrashDuringFirstJudgmentNeverReinvokesJudge(t *testing.T) {
	service, _, _ := testService(t, domain.ModeShadow)
	calls := 0
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		calls++
		panic("simulated crash during pooled first attempt")
	}}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected simulated crash")
			}
		}()
		_, _ = service.Start(context.Background(), domain.ModeShadow)
	}()
	cycle, err := service.ResumeObservation(context.Background(), "cycle-1")
	if err == nil || !strings.Contains(err.Error(), "indeterminate") || calls != 1 || cycle.Stage != domain.StageFailed {
		t.Fatalf("stage=%s calls=%d err=%v", cycle.Stage, calls, err)
	}
}

// Mirrors the cycle-20260926T085137Z-c6c4ae97 shape: an unselected
// duplicate canary with a minimizing target above its baseline.
func TestInvalidNoOpOpportunityIsRejectedAndSigned(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeProposal)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		judgment.SelectedIndex = nil
		judgment.NoOpReason = "Duplicate of a successful prior outcome."
		judgment.Opportunities[0].Contract.Metric.Baseline = 3397.985
		judgment.Opportunities[0].Contract.Metric.Target = 60000
		return judgment
	}}
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err == nil || cycle.Stage != domain.StageFailed || !strings.Contains(err.Error(), "no valid opportunities") {
		t.Fatalf("cycle stage=%s error=%v", cycle.Stage, err)
	}
	if len(cycle.JudgmentRejections) != 1 || cycle.JudgmentRejections[0].Index != 0 ||
		!strings.Contains(cycle.JudgmentRejections[0].Reason, "target must be below baseline") {
		t.Fatalf("rejections = %#v", cycle.JudgmentRejections)
	}
	if cycle.JudgmentRetry == nil || cycle.JudgmentRetry.Outcome != "rejected" || len(cycle.JudgmentRetry.Rejections) != 1 {
		t.Fatalf("retry = %#v", cycle.JudgmentRetry)
	}
	if backlog.createCalls != 0 {
		t.Fatalf("backlog calls=%d", backlog.createCalls)
	}
	cycle, err = service.Compound(context.Background(), cycle.ID)
	if err != nil || cycle.SignedReceiptID == "" {
		t.Fatalf("compound error=%v receipt=%q", err, cycle.SignedReceiptID)
	}
	var receipt domain.Receipt
	if err := service.Store.ReadJSON(cycle.ID, "receipt.json", &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Cycle.JudgmentRejections) != 1 || receipt.Cycle.JudgmentRejections[0] != cycle.JudgmentRejections[0] {
		t.Fatalf("receipt rejections = %#v", receipt.Cycle.JudgmentRejections)
	}
	if receipt.Cycle.JudgmentRetry == nil || receipt.Cycle.JudgmentRetry.Outcome != "rejected" {
		t.Fatalf("receipt retry = %#v", receipt.Cycle.JudgmentRetry)
	}
}

func TestInvalidOpportunityDoesNotDiscardValidNoOp(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeProposal)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		invalid := judgment.Opportunities[0]
		invalid.Contract.Metric.Baseline = 3397.985
		invalid.Contract.Metric.Target = 60000
		judgment.Opportunities = []domain.Candidate{invalid, judgment.Opportunities[0]}
		judgment.SelectedIndex = nil
		judgment.NoOpReason = "Neither opportunity clears the execution threshold."
		return judgment
	}}
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil || cycle.Stage != domain.StageCompleted {
		t.Fatalf("cycle stage=%s error=%v", cycle.Stage, err)
	}
	if len(cycle.JudgmentRejections) != 1 || len(cycle.Judgment.Opportunities) != 1 || backlog.createCalls != 0 {
		t.Fatalf("rejections=%#v judgment=%#v calls=%d", cycle.JudgmentRejections, cycle.Judgment, backlog.createCalls)
	}
}

func TestFilteredJudgmentIsCanonicalAndReplayable(t *testing.T) {
	service, _, _ := testService(t, domain.ModeShadow)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		invalid := judgment.Opportunities[0]
		invalid.Contract.Metric.Target = 60000
		judgment.Opportunities = []domain.Candidate{invalid, judgment.Opportunities[0]}
		return judgment
	}}
	cycle, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil || cycle.Stage != domain.StageCompleted {
		t.Fatalf("cycle stage=%s error=%v", cycle.Stage, err)
	}
	var stored domain.Judgment
	if err := service.Store.ReadJSON(cycle.ID, "judgment.json", &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Opportunities) != 1 || stored.Opportunities[0].Contract.Metric.Target != 80 {
		t.Fatalf("stored judgment = %#v", stored)
	}
	var observation Observation
	if err := service.Store.ReadJSON(cycle.ID, "observation.json", &observation); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReplayJudgment(stored, observation); err != nil {
		t.Fatalf("replay judgment: %v", err)
	}
	_, _, _, err = service.loadOrRunJudgment(context.Background(), &cycle, observation, nil)
	if err != nil || len(cycle.JudgmentRejections) != 1 || cycle.JudgmentRejectionAttempt != 1 {
		t.Fatalf("reload error=%v rejections=%#v attempt=%d", err, cycle.JudgmentRejections, cycle.JudgmentRejectionAttempt)
	}
}

func TestSurvivingSelectionKeepsOriginalIndex(t *testing.T) {
	service, _, _ := testService(t, domain.ModeShadow)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		judgment.Opportunities = []domain.Candidate{judgment.Opportunities[0], judgment.Opportunities[0]}
		index := 1
		judgment.SelectedIndex = &index
		return judgment
	}}
	cycle, err := service.Start(context.Background(), domain.ModeShadow)
	if err != nil || cycle.Judgment == nil || cycle.Judgment.SelectedIndex == nil || *cycle.Judgment.SelectedIndex != 1 {
		t.Fatalf("cycle judgment=%#v error=%v", cycle.Judgment, err)
	}
}

func TestRejectedSelectionPromotesHighestValidOpportunity(t *testing.T) {
	service, _, backlog := testService(t, domain.ModeProposal)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		invalid := judgment.Opportunities[0]
		invalid.Contract.Metric.Target = 60000
		judgment.Opportunities = []domain.Candidate{invalid, judgment.Opportunities[0]}
		return judgment
	}}
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil || cycle.Stage != domain.StageAwaitingApproval {
		t.Fatalf("cycle stage=%s error=%v", cycle.Stage, err)
	}
	if len(cycle.JudgmentRejections) != 1 || !cycle.JudgmentRejections[0].Selected || cycle.Judgment.SelectedIndex == nil || *cycle.Judgment.SelectedIndex != 0 || backlog.createCalls != 1 {
		t.Fatalf("rejections=%#v judgment=%#v calls=%d", cycle.JudgmentRejections, cycle.Judgment, backlog.createCalls)
	}
	if cycle.Candidate.Contract.Metric.Target != 80 {
		t.Fatalf("selected invalid candidate: %#v", cycle.Candidate)
	}
}
