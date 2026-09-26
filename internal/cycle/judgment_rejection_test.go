package cycle

import (
	"context"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

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
	if len(cycle.JudgmentRejections) != 1 || cycle.Judgment.SelectedIndex == nil || *cycle.Judgment.SelectedIndex != 0 || backlog.createCalls != 1 {
		t.Fatalf("rejections=%#v judgment=%#v calls=%d", cycle.JudgmentRejections, cycle.Judgment, backlog.createCalls)
	}
	if cycle.Candidate.Contract.Metric.Target != 80 {
		t.Fatalf("selected invalid candidate: %#v", cycle.Candidate)
	}
}
