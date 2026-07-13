package cycle

import (
	"context"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

func TestDeclineRecordsPrincipalClosesExperimentAndSignsTerminalCycle(t *testing.T) {
	service, kernel, backlog := testService(t, domain.ModeProposal)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}

	declined, err := service.Decline(context.Background(), cycle.ID, "mk", "The expected leverage is too low.")
	if err != nil {
		t.Fatal(err)
	}
	if declined.Stage != domain.StageCompleted || declined.Decline == nil || declined.Decline.Actor != "mk" || declined.Decline.Reason == "" {
		t.Fatalf("declined cycle = %#v", declined)
	}
	if backlog.closeCalls != 1 {
		t.Fatalf("close calls = %d", backlog.closeCalls)
	}
	if _, err := service.Decline(context.Background(), cycle.ID, "mk", "The expected leverage is too low."); err != nil {
		t.Fatal(err)
	}
	if backlog.closeCalls != 1 {
		t.Fatalf("repeat decline closed %d times", backlog.closeCalls)
	}

	signed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if signed.SignedReceiptID == "" || kernel.phase != "compound" || kernel.runStatus != "completed" {
		t.Fatalf("signed=%#v phase=%s status=%s", signed, kernel.phase, kernel.runStatus)
	}
}

func TestDeclineRejectsSelfApprovalAndBlankReason(t *testing.T) {
	service, _, _ := testService(t, domain.ModeProposal)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		actor  string
		reason string
	}{
		{actor: "remontoire", reason: "automated decline"},
		{actor: "mk", reason: "  "},
	} {
		if _, err := service.Decline(context.Background(), cycle.ID, test.actor, test.reason); err == nil || !strings.Contains(err.Error(), "decline") {
			t.Fatalf("actor=%q reason=%q error=%v", test.actor, test.reason, err)
		}
	}
}
