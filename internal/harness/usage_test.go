package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

// testdata/codex holds one real Codex executor run (cycle of 2026-07-13) with
// message text, command output, paths and the thread ID redacted; see
// testdata/codex/README.md. The token counts are the ones Codex reported.
func codexFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "codex", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func wantTokens(t *testing.T, usage domain.Usage, input, cached, output, reasoning *int64) {
	t.Helper()
	for name, pair := range map[string][2]*int64{
		"input": {usage.InputTokens, input}, "cached": {usage.CachedInputTokens, cached},
		"output": {usage.OutputTokens, output}, "reasoning": {usage.ReasoningOutputTokens, reasoning},
	} {
		got, want := pair[0], pair[1]
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%s tokens = %v, want %v", name, deref(got), deref(want))
		}
	}
}

func deref(v *int64) any {
	if v == nil {
		return "unknown"
	}
	return *v
}

func TestCodexUsageFromRedactedTranscript(t *testing.T) {
	usage := codexUsage(codexFixture(t, "executor.jsonl"))
	wantTokens(t, usage, domain.Int64(202151), domain.Int64(153088), domain.Int64(1779), domain.Int64(470))
	if usage.CostUSD != nil {
		t.Fatalf("Codex reports no dollar cost, got %v", *usage.CostUSD)
	}
}

func TestCodexUsageSumsTurnsAndKeepsMissingFieldsUnknown(t *testing.T) {
	two := `{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2,"reasoning_output_tokens":1}}
{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":3}}
`
	usage := codexUsage([]byte(two))
	wantTokens(t, usage, domain.Int64(15), nil, domain.Int64(5), nil)

	zero := `{"type":"turn.started"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0}}`
	wantTokens(t, codexUsage([]byte(zero)), domain.Int64(0), domain.Int64(0), domain.Int64(0), domain.Int64(0))
}

func TestCodexUsageIsUnknownWithoutAUsableCompletedTurn(t *testing.T) {
	for name, transcript := range map[string]string{
		"empty":               "",
		"started only":        `{"type":"turn.started"}` + "\n",
		"completed no usage":  `{"type":"turn.started"}` + "\n" + `{"type":"turn.completed"}` + "\n",
		"negative count":      `{"type":"turn.started"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":-1}}` + "\n",
		"completed no start":  `{"type":"turn.completed","usage":{"input_tokens":1}}` + "\n",
		"unterminated stream": `{"type":"turn.started"}` + "\n" + `{"type":"item.completed","item":{"id":"x"`,
	} {
		if usage := codexUsage([]byte(transcript)); usage != (domain.Usage{}) {
			t.Errorf("%s: usage = %+v, want all unknown", name, usage)
		}
	}
	long := `{"type":"item.completed","item":{"aggregated_output":"` + strings.Repeat("x", 9<<20) + `"}}` + "\n" +
		`{"type":"turn.started"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":7}}` + "\n"
	if usage := codexUsage([]byte(long)); usage != (domain.Usage{}) {
		t.Errorf("oversized line: usage = %+v, want all unknown", usage)
	}
}

func TestCodexUsageUnreadableCompletionAfterValidTurnIsUnknown(t *testing.T) {
	valid := `{"type":"turn.started"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":1}}` + "\n"
	for name, tail := range map[string]string{
		"truncated completion": `{"type":"turn.completed","usage":{"input_tokens":4`,
		"cut type":             `{"type":"turn.comple`,
		"started no finish":    `{"type":"turn.started"}`,
		"turn failed":          `{"type":"turn.started"}` + "\n" + `{"type":"turn.failed","error":{"message":"x"}}`,
		"stray text line":      `warning: not json`,
		"garbled completion":   `{"type":"turn.completed","usage":`,
	} {
		if usage := codexUsage([]byte(valid + tail)); usage != (domain.Usage{}) {
			t.Errorf("%s: usage = %+v, want all unknown", name, usage)
		}
	}
	// A bad field poisons only that field; the others stay known.
	for name, bad := range map[string]string{"string": `"bad"`, "fraction": `1.5`, "negative": `-3`, "null": `null`} {
		usage := codexUsage([]byte(valid + `{"type":"turn.started"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":` + bad + `,"output_tokens":2}}` + "\n"))
		wantTokens(t, usage, nil, nil, domain.Int64(3), nil)
		if t.Failed() {
			t.Fatalf("%s input_tokens", name)
		}
	}
	oversized := `{"type":"item.completed","item":{"aggregated_output":"` + strings.Repeat("x", 9<<20) + `"}}` + "\n"
	if usage := codexUsage([]byte(valid + oversized)); usage != (domain.Usage{}) {
		t.Errorf("valid turn then oversized line: usage = %+v, want all unknown", usage)
	}
}

func TestTokenSumsDetectOverflow(t *testing.T) {
	max := int64(math.MaxInt64)
	line := func(n int64) string {
		return fmt.Sprintf(`{"type":"turn.started"}`+"\n"+`{"type":"turn.completed","usage":{"input_tokens":%d,"output_tokens":1}}`+"\n", n)
	}
	usage := codexUsage([]byte(line(max) + line(max) + line(2)))
	wantTokens(t, usage, nil, nil, domain.Int64(3), nil)
	usage = codexUsage([]byte(line(max-1) + line(1)))
	wantTokens(t, usage, domain.Int64(max), nil, domain.Int64(2), nil)

	a, b := domain.Usage{InputTokens: domain.Int64(max)}, domain.Usage{InputTokens: domain.Int64(2)}
	if got := a.Combine(b); got.InputTokens != nil {
		t.Fatalf("overflowing Combine = %d, want unknown", *got.InputTokens)
	}
}

func TestCodexExecuteReportsTokensAndUnknownCost(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "execution.json")
	if err := os.WriteFile(output, []byte(executionReportJSON()), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{responses: []adapters.Result{{Stdout: codexFixture(t, "executor.jsonl"), Stderr: []byte(poolStartMarker + "\n")}}}
	backend := Codex{Binary: "codex", Model: "gpt-5.4", PoolInputDir: dir, Runner: runner}
	_, meta, err := backend.Execute(context.Background(), ExecutionRequest{
		Worktree: "/worktree", SchemaPath: "/schemas/execution.json", OutputPath: output, Contract: harnessContract(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTokens(t, meta.Usage, domain.Int64(202151), domain.Int64(153088), domain.Int64(1779), domain.Int64(470))
	if meta.CostUSD != nil {
		t.Fatalf("cost = %v, want unknown", *meta.CostUSD)
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cost_usd") {
		t.Fatalf("unknown cost serialized: %s", encoded)
	}
}

func TestAttemptRecordKeepsUnknownCostUnknown(t *testing.T) {
	// The record Remontoire wrote before usage was tracked: no cost, no tokens.
	var record domain.ExecutionRecord
	if err := json.Unmarshal(codexFixture(t, "attempt-record.json"), &record); err != nil {
		t.Fatal(err)
	}
	if record.Usage != (domain.Usage{}) || record.Turns != 1 {
		t.Fatalf("legacy record: turns=%d usage=%+v, want 1 turn and unknown usage", record.Turns, record.Usage)
	}
	legacy, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cost_usd", "input_tokens", "output_tokens"} {
		if strings.Contains(string(legacy), key) {
			t.Fatalf("unknown %s serialized as a value: %s", key, legacy)
		}
	}

	record.Usage = codexUsage(codexFixture(t, "executor.jsonl"))
	filled, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var round domain.ExecutionRecord
	if err := json.Unmarshal(filled, &round); err != nil {
		t.Fatal(err)
	}
	wantTokens(t, round.Usage, domain.Int64(202151), domain.Int64(153088), domain.Int64(1779), domain.Int64(470))
	if strings.Contains(string(filled), "cost_usd") || round.CostUSD != nil {
		t.Fatalf("cost invented on round trip: %s", filled)
	}

	record.CostUSD = domain.Float(0)
	reported, _ := json.Marshal(record)
	if !strings.Contains(string(reported), `"cost_usd":0`) {
		t.Fatalf("a reported zero cost must survive serialization: %s", reported)
	}
}

func TestClaudeCostMissingIsUnknownAndZeroIsKnown(t *testing.T) {
	var target domain.Review
	_, cost, _ := decodeClaudeWithUsage([]byte(`{"is_error":true}`), &target)
	if cost != nil {
		t.Fatalf("missing total_cost_usd parsed as %v, want unknown", *cost)
	}
	_, cost, _ = decodeClaudeWithUsage([]byte(`{"is_error":true,"total_cost_usd":0}`), &target)
	if cost == nil || *cost != 0 {
		t.Fatalf("reported zero cost = %v, want known zero", cost)
	}
	_, cost, _ = decodeClaudeWithUsage([]byte(`not json`), &target)
	if cost != nil {
		t.Fatal("undecodable envelope must leave cost unknown")
	}
}

func TestUsageCombineNeverTurnsUnknownIntoASmallerTotal(t *testing.T) {
	known := domain.Usage{CostUSD: domain.Float(0.25), InputTokens: domain.Int64(3)}
	got := known.Combine(domain.Usage{CostUSD: domain.Float(0.5), InputTokens: domain.Int64(4)})
	if got.CostUSD == nil || *got.CostUSD != 0.75 || got.InputTokens == nil || *got.InputTokens != 7 {
		t.Fatalf("combined known usage = %+v", got)
	}
	if got := known.Combine(domain.Usage{}); got != (domain.Usage{}) {
		t.Fatalf("known + unknown = %+v, want unknown", got)
	}
}

func TestCostValidityKeepsBadCostsUnknown(t *testing.T) {
	var target domain.Review
	for name, body := range map[string]string{"negative": `{"is_error":true,"total_cost_usd":-0.25}`, "string": `{"is_error":true,"total_cost_usd":"x"}`} {
		_, cost, _ := decodeClaudeWithUsage([]byte(body), &target)
		if name == "negative" && cost != nil {
			t.Errorf("%s cost parsed as %v, want unknown", name, *cost)
		}
	}
	good, bad := domain.Usage{CostUSD: domain.Float(0.25)}, domain.Usage{CostUSD: domain.Float(-0.25)}
	if got := good.Combine(bad); got.CostUSD != nil {
		t.Fatalf("0.25 + -0.25 = %v, want unknown, not a known zero", *got.CostUSD)
	}
	huge := domain.Usage{CostUSD: domain.Float(math.MaxFloat64)}
	if got := huge.Combine(huge); got.CostUSD != nil {
		t.Fatalf("overflowing cost sum = %v, want unknown", *got.CostUSD)
	}
}

func TestCodexUsageRejectsMisorderedAmbiguousAndUntypedStreams(t *testing.T) {
	start, done := `{"type":"turn.started"}`+"\n", func(n int) string {
		return fmt.Sprintf(`{"type":"turn.completed","usage":{"input_tokens":%d}}`+"\n", n)
	}
	for name, transcript := range map[string]string{
		"equal counts but last turn open": start + done(10) + done(20) + start,
		"nested start":                    start + start + done(1) + done(2),
		"null event":                      start + "null\n" + done(1),
		"untyped object":                  start + "{}\n" + done(1),
		"duplicate field null then zero":  start + `{"type":"turn.completed","usage":{"input_tokens":null,"input_tokens":0}}` + "\n",
		"duplicate type":                  start + `{"type":"turn.failed","type":"turn.completed","usage":{"input_tokens":1}}` + "\n",
	} {
		if usage := codexUsage([]byte(transcript)); usage != (domain.Usage{}) {
			t.Errorf("%s: usage = %+v, want all unknown", name, usage)
		}
	}
}

func TestCodexUsageIsUnknownWhenTheRunErrored(t *testing.T) {
	valid := []byte(`{"type":"turn.started"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":10}}` + "\n")
	if usage := codexRunUsage(valid, nil); usage.InputTokens == nil || *usage.InputTokens != 10 {
		t.Fatalf("clean run usage = %+v, want 10 input tokens", usage)
	}
	if usage := codexRunUsage(valid, adapters.ErrOutputLimit); usage != (domain.Usage{}) {
		t.Fatalf("truncated run usage = %+v, want all unknown", usage)
	}
	// End to end: a valid prefix plus ErrOutputLimit must not read as known usage.
	runner := &fakeRunner{responses: []adapters.Result{{Stdout: valid, Stderr: []byte(poolStartMarker + "\n")}}, errors: []error{adapters.ErrOutputLimit}}
	backend := Codex{Binary: "codex", Model: "gpt-5.4", PoolInputDir: t.TempDir(), Runner: runner}
	_, meta, err := backend.Execute(context.Background(), ExecutionRequest{
		Worktree: "/worktree", SchemaPath: "/schemas/execution.json", OutputPath: filepath.Join(t.TempDir(), "o.json"), Contract: harnessContract(),
	})
	if err == nil {
		t.Fatal("want the output-limit error")
	}
	if meta.Usage != (domain.Usage{}) {
		t.Fatalf("usage after output limit = %+v, want all unknown", meta.Usage)
	}
}

func TestClaudeEnvelopeDuplicateCostKeysStayUnknown(t *testing.T) {
	var target domain.Review
	_, cost, err := decodeClaudeWithUsage([]byte(`{"total_cost_usd":null,"total_cost_usd":0,"is_error":true}`), &target)
	if err == nil || cost != nil {
		t.Fatalf("duplicate cost keys: cost = %v err = %v, want unknown cost and an error", cost, err)
	}
}

func TestUsageNormalizeTurnsInvalidRecordedValuesIntoUnknown(t *testing.T) {
	got := domain.Usage{CostUSD: domain.Float(-0.25), InputTokens: domain.Int64(-1), OutputTokens: domain.Int64(5)}.Normalize()
	if got.CostUSD != nil || got.InputTokens != nil || got.OutputTokens == nil || *got.OutputTokens != 5 {
		t.Fatalf("normalized = %+v, want bad cost and input unknown, output 5 kept", got)
	}
	if kept := (domain.Usage{CostUSD: domain.Float(0)}).Normalize(); kept.CostUSD == nil {
		t.Fatal("a reported zero cost must stay known")
	}
}
