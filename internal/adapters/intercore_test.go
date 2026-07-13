package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

func TestIntercoreLockAndStateArgv(t *testing.T) {
	runner := &recordingRunner{}
	ic := Intercore{Binary: "/opt/bin/ic", Dir: "/portfolio", Runner: runner}
	ctx := context.Background()

	if err := ic.AcquireCycleLock(ctx, "sylveste", "remontoire:123", "2s"); err != nil {
		t.Fatal(err)
	}
	cycle := domain.Cycle{SchemaVersion: domain.CycleSchemaV1, ID: "cycle-1", Portfolio: "sylveste", Stage: domain.StageObserving}
	if err := ic.SetCycle(ctx, cycle); err != nil {
		t.Fatal(err)
	}
	if err := ic.ReleaseCycleLock(ctx, "sylveste", "remontoire:123"); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"lock", "acquire", "remontoire-cycle", "sylveste", "--timeout=2s", "--owner=remontoire:123"},
		{"state", "set", "remontoire.cycle", "cycle-1"},
		{"lock", "release", "remontoire-cycle", "sylveste", "--owner=remontoire:123"},
	}
	if len(runner.calls) != len(want) {
		t.Fatalf("calls = %d, want %d", len(runner.calls), len(want))
	}
	for i := range want {
		if got := runner.calls[i].Invocation.Args; !reflect.DeepEqual(got, want[i]) {
			t.Errorf("call %d args = %#v, want %#v", i, got, want[i])
		}
		if runner.calls[i].Invocation.Name != ic.Binary || runner.calls[i].Invocation.Dir != ic.Dir {
			t.Errorf("call %d command = %#v", i, runner.calls[i].Invocation)
		}
	}
	var persisted domain.Cycle
	if err := json.Unmarshal(runner.calls[1].Invocation.Stdin, &persisted); err != nil {
		t.Fatalf("state stdin: %v", err)
	}
	if persisted.ID != cycle.ID || persisted.Stage != cycle.Stage {
		t.Fatalf("persisted cycle = %#v", persisted)
	}
}

func TestIntercoreLockHeldIsTyped(t *testing.T) {
	runner := &recordingRunner{}
	runner.queueExit(1, "")
	ic := Intercore{Binary: "ic", Runner: runner}
	err := ic.AcquireCycleLock(context.Background(), "sylveste", "owner", "0s")
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("error = %v, want ErrLockHeld", err)
	}
}

func TestIntercoreRunReplayEventAndReceiptArgv(t *testing.T) {
	runner := &recordingRunner{}
	runner.queue(`{"id":"run-123"}`)
	runner.queue("42\n")
	runner.queue("event-7\n")
	runner.queue(`{"receipt_id":"rcpt-9","key_id":"key-1","agent_id":"remontoire"}`)
	ic := Intercore{Binary: "ic", Dir: "/portfolio", Runner: runner}
	ctx := context.Background()

	runID, err := ic.CreateCycleRun(ctx, "/repo", "cycle-1", map[string]any{"mode": "proposal"})
	if err != nil {
		t.Fatal(err)
	}
	if runID != "run-123" {
		t.Fatalf("run id = %q", runID)
	}
	if err := ic.RecordReplayInput(ctx, runID, "beads", "beads.json", `{"sha256":"abc"}`, ".remontoire/cycles/cycle-1/beads.json"); err != nil {
		t.Fatal(err)
	}
	if err := ic.RecordStageEvent(ctx, runID, "/repo", domain.StageObserving, "cycle-1"); err != nil {
		t.Fatal(err)
	}
	receiptID, err := ic.EmitReceipt(ctx, runID, "codex", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if receiptID != "rcpt-9" {
		t.Fatalf("receipt id = %q", receiptID)
	}

	wantCalls := [][]string{
		{"--json", "run", "create", "--project=/repo", "--goal=Remontoire portfolio cycle cycle-1", "--scope-id=cycle-1", `--phases=["observe","rank","propose","execute","review","compound"]`, `--metadata={"mode":"proposal"}`},
		{"run", "replay", "record", "run-123", "--kind=beads", "--key=beads.json", `--payload={"sha256":"abc"}`, "--artifact-ref=.remontoire/cycles/cycle-1/beads.json"},
		{"events", "record", "--source=interspect", "--type=remontoire.stage", "--run=run-123", "--project=/repo", `--payload={"agent_name":"remontoire","context":"{\"cycle_id\":\"cycle-1\",\"stage\":\"observing\"}"}`},
		{"--json", "receipt", "emit", "--agent=remontoire", "--model=codex", "--content-hash=" + strings.Repeat("a", 64), "--parent-run=run-123"},
	}
	for i, want := range wantCalls {
		if got := runner.calls[i].Invocation.Args; !reflect.DeepEqual(got, want) {
			t.Errorf("call %d args = %#v, want %#v", i, got, want)
		}
	}
}

func TestIntercoreReadsCanonicalObservation(t *testing.T) {
	runner := &recordingRunner{}
	runner.queue(`[{"id":"disc-1","title":"A"}]`)
	runner.queue(`{"keyword_weights":"{}","source_weights":"{}"}`)
	runner.queue(`{"schema_version":"remontoire.cycle/v1","id":"cycle-1","portfolio":"sylveste","mode":"shadow","stage":"completed","created_at":"2026-07-13T00:00:00Z","updated_at":"2026-07-13T00:00:00Z"}`)
	ic := Intercore{Binary: "ic", Runner: runner}

	discoveries, profile, err := ic.Observation(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(discoveries) != 1 || discoveries[0].ID != "disc-1" || profile.KeywordWeights != "{}" {
		t.Fatalf("observation = %#v %#v", discoveries, profile)
	}
	cycle, err := ic.GetCycle(context.Background(), "cycle-1")
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Stage != domain.StageCompleted {
		t.Fatalf("cycle stage = %s", cycle.Stage)
	}

	want := [][]string{
		{"--json", "discovery", "list", "--limit=50"},
		{"--json", "discovery", "profile"},
		{"state", "get", "remontoire.cycle", "cycle-1"},
	}
	for i := range want {
		if got := runner.calls[i].Invocation.Args; !reflect.DeepEqual(got, want[i]) {
			t.Errorf("call %d = %#v, want %#v", i, got, want[i])
		}
	}
}
