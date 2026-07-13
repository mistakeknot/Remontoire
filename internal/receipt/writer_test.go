package receipt

import (
	"strings"
	"testing"
	"time"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

func TestBuildProducesStableTerminalReceiptWithoutSelfReference(t *testing.T) {
	terminal := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	cycle := domain.Cycle{
		SchemaVersion: domain.CycleSchemaV1,
		ID:            "cycle-1", RunID: "run-1", Portfolio: "sylveste", Mode: domain.ModeProposal,
		Stage: domain.StageCompounding, CreatedAt: terminal.Add(-time.Hour), UpdatedAt: terminal.Add(-time.Minute),
		ContractHash: strings.Repeat("a", 64), CandidateHash: strings.Repeat("b", 64),
		SignedReceiptID: "must-not-be-self-referenced",
		Artifacts: []domain.Artifact{
			{Kind: "patch", Path: "/cycles/cycle-1/experiment.patch", Digest: strings.Repeat("c", 64)},
			{Kind: "beads", Path: "/cycles/cycle-1/beads.json", Digest: strings.Repeat("d", 64)},
		},
	}

	first, err := Build(cycle, terminal)
	if err != nil {
		t.Fatal(err)
	}
	cycle.Artifacts[0], cycle.Artifacts[1] = cycle.Artifacts[1], cycle.Artifacts[0]
	second, err := Build(cycle, terminal)
	if err != nil {
		t.Fatal(err)
	}
	if first.DecisionHash != second.DecisionHash {
		t.Fatalf("decision hash depends on artifact ordering: %s != %s", first.DecisionHash, second.DecisionHash)
	}
	if first.Cycle.Stage != domain.StageCompleted || !first.Cycle.UpdatedAt.Equal(terminal) || first.Cycle.SignedReceiptID != "" {
		t.Fatalf("terminal cycle projection = %#v", first.Cycle)
	}
	if len(first.InputArtifacts) != 1 || first.InputArtifacts[0].Kind != "beads" || len(first.OutputArtifacts) != 1 || first.OutputArtifacts[0].Kind != "patch" {
		t.Fatalf("artifact partition = %#v %#v", first.InputArtifacts, first.OutputArtifacts)
	}
	data, digest, err := Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || len(digest) != 64 || strings.Contains(string(data), "must-not-be-self-referenced") {
		t.Fatalf("marshaled receipt digest=%q data=%s", digest, data)
	}
}

func TestBuildPreservesFailedTerminalStage(t *testing.T) {
	terminal := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	cycle := domain.Cycle{
		SchemaVersion: domain.CycleSchemaV1,
		ID:            "cycle-failed", Portfolio: "sylveste", Mode: domain.ModeProposal,
		Stage: domain.StageFailed, CreatedAt: terminal.Add(-time.Minute), Failure: "canonical backlog unavailable",
	}
	receipt, err := Build(cycle, terminal)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Cycle.Stage != domain.StageFailed || receipt.Cycle.Failure != cycle.Failure {
		t.Fatalf("terminal cycle = %#v", receipt.Cycle)
	}
}
