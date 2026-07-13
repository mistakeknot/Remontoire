package adapters

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

func adapterCandidate() domain.Candidate {
	return domain.Candidate{
		Title:    "Measure roadmap parse cost",
		Summary:  "Test whether roadmap parsing is a material refresh bottleneck.",
		Project:  "Remontoire",
		Priority: 4,
		Evidence: []domain.EvidenceRef{{Kind: "discovery", ID: "disc-1", Digest: strings.Repeat("a", 64)}},
		Contract: domain.EvidenceContract{
			SchemaVersion: domain.ContractSchemaV1,
			Hypothesis:    "Parsing dominates refresh time.",
			Falsifier:     "Parsing is below ten percent of refresh time.",
			Repository:    "/repo",
			AllowedPaths:  []string{"internal/roadmap"},
			Metric: domain.Metric{
				Name: "parse_ms", Unit: "ms", Direction: domain.DirectionMinimize, Baseline: 100, Target: 80,
			},
			Benchmark:         []string{"go", "test", "./internal/roadmap"},
			Budget:            domain.Budget{MaxDurationSeconds: 300, MaxTurns: 5, MaxCostUSD: 2},
			StopConditions:    []string{"tests fail"},
			Executor:          "codex",
			PromotionCriteria: "target met",
			ClosureCriteria:   "target missed",
		},
	}
}

func TestBeadsListAndFingerprintDedup(t *testing.T) {
	fingerprint := strings.Repeat("f", 64)
	runner := &recordingRunner{}
	runner.queue(`[{"id":"Revel-1","title":"Existing","status":"open","priority":4,"labels":["remontoire:fingerprint:` + fingerprint + `"]}]`)
	beads := Beads{Binary: "bd", Dir: "/portfolio", Runner: runner}

	items, err := beads.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !HasFingerprint(items, fingerprint) {
		t.Fatal("existing fingerprint was not detected")
	}
	want := []string{"list", "--all", "--limit=0", "--json"}
	if got := runner.calls[0].Invocation.Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestBeadsCreateExperimentIsP4AndCarriesIdempotencyLabel(t *testing.T) {
	runner := &recordingRunner{}
	runner.queue("Revel-exp\n")
	beads := Beads{Binary: "bd", Dir: "/portfolio", Runner: runner}
	candidate := adapterCandidate()
	fingerprint := strings.Repeat("f", 64)

	id, err := beads.CreateExperiment(context.Background(), "cycle-1", fingerprint, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if id != "Revel-exp" {
		t.Fatalf("id = %q", id)
	}
	args := runner.calls[0].Invocation.Args
	wantPrefix := []string{"create", "--silent", "--title=[Experiment] Measure roadmap parse cost", "--type=task", "--priority=P4"}
	if !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("args prefix = %#v, want %#v", args[:len(wantPrefix)], wantPrefix)
	}
	if !containsArg(args, "--labels=remontoire-experiment,remontoire:cycle:cycle-1,remontoire:fingerprint:"+fingerprint) {
		t.Fatalf("missing idempotency labels: %#v", args)
	}
	metadataArg := findArgPrefix(args, "--metadata=")
	var metadata map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(metadataArg, "--metadata=")), &metadata); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if metadata["cycle_id"] != "cycle-1" || metadata["fingerprint"] != fingerprint {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func findArgPrefix(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return arg
		}
	}
	return ""
}
