package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

type fakeRunner struct {
	calls     []adapters.Invocation
	responses []adapters.Result
}

func (r *fakeRunner) Run(_ context.Context, invocation adapters.Invocation) (adapters.Result, error) {
	r.calls = append(r.calls, invocation)
	if len(r.responses) == 0 {
		return adapters.Result{}, nil
	}
	result := r.responses[0]
	r.responses = r.responses[1:]
	return result, nil
}

func noOpJudgmentJSON() string {
	return `{"schema_version":"remontoire.judgment/v1","opportunities":[],"selected_index":null,"no_op_reason":"No bounded evidence-backed opportunity."}`
}

func executionReportJSON() string {
	return `{"schema_version":"remontoire.execution/v1","summary":"Added a benchmark fixture.","changed_paths":["internal/roadmap/cache_test.go"],"commands":["go test ./internal/roadmap"],"completed":true}`
}

func reviewJSON() string {
	return `{"schema_version":"remontoire.review/v1","contract_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","verdict":"close_success","rationale":"The bounded evidence is internally consistent.","evidence":[{"kind":"measurement","id":"measurement.json","digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`
}

func harnessContract() domain.EvidenceContract {
	return domain.EvidenceContract{
		SchemaVersion: domain.ContractSchemaV1,
		Hypothesis:    "A cache reduces refresh time.",
		Falsifier:     "The target is missed or tests fail.",
		Repository:    "/repo",
		AllowedPaths:  []string{"internal/roadmap"},
		Metric: domain.Metric{
			Name: "refresh_ms", Unit: "ms", Direction: domain.DirectionMinimize, Source: domain.MetricSourceWallDurationMS, Baseline: 100, Target: 80,
		},
		Benchmark:         []string{"go", "test", "./internal/roadmap"},
		Budget:            domain.Budget{MaxDurationSeconds: 600, MaxTurns: 6, MaxCostUSD: 2.5},
		StopConditions:    []string{"tests fail", "path boundary crossed"},
		Executor:          "codex",
		PromotionCriteria: "target met and tests pass",
		ClosureCriteria:   "target missed or tests fail",
	}
}

func TestSanitizeObservationRedactsSecretsAndBoundsInput(t *testing.T) {
	raw := []byte(`{"title":"safe","api_token":"ghp_abcdefghijklmnopqrstuvwxyz1234567890","nested":{"password":"not-for-model","text":"key sk-abcdefghijklmnopqrstuvwxyz123456"}}`)
	got, err := SanitizeObservation(raw, 4096)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, forbidden := range []string{"ghp_", "not-for-model", "sk-abcdef"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized observation still contains %q: %s", forbidden, text)
		}
	}
	if strings.Count(text, "[REDACTED]") < 3 {
		t.Fatalf("expected redactions: %s", text)
	}
	if _, err := SanitizeObservation(raw, 8); err == nil {
		t.Fatal("oversized observation was accepted")
	}
}

func TestSafeEnvironmentRehomesCacheUnderTempDir(t *testing.T) {
	originalEnviron := environ
	t.Cleanup(func() { environ = originalEnviron })
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	hostileTarget := t.TempDir()
	hostilePath := filepath.Join(tempDir, "remontoire-cache-hostile")
	if err := os.Symlink(hostileTarget, hostilePath); err != nil {
		t.Fatal(err)
	}
	environ = func() []string {
		return []string{
			"HOME=/home/mk",
			"PATH=/usr/local/go/bin:/usr/bin:/bin",
			"XDG_CACHE_HOME=/home/mk/.cache",
			"REMONTOIRE_SECRET=not-for-children",
		}
	}

	environment, cleanup, err := safeEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var cacheHome string
	for _, entry := range environment {
		if strings.HasPrefix(entry, "REMONTOIRE_SECRET=") {
			t.Fatalf("safe environment leaked secret: %s", entry)
		}
		if value, ok := strings.CutPrefix(entry, "XDG_CACHE_HOME="); ok {
			cacheHome = value
		}
	}
	if filepath.Dir(cacheHome) != tempDir || !strings.HasPrefix(filepath.Base(cacheHome), "remontoire-cache-") {
		t.Fatalf("XDG_CACHE_HOME = %q, want process cache under %q", cacheHome, tempDir)
	}
	if cacheHome == hostilePath {
		t.Fatalf("XDG_CACHE_HOME reused hostile path %q", hostilePath)
	}
	info, err := os.Lstat(cacheHome)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("cache mode = %v, want private directory 0700", info.Mode())
	}

	secondEnvironment, secondCleanup, err := safeEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer secondCleanup()
	var secondCacheHome string
	for _, entry := range secondEnvironment {
		if value, ok := strings.CutPrefix(entry, "XDG_CACHE_HOME="); ok {
			secondCacheHome = value
		}
	}
	if secondCacheHome == cacheHome {
		t.Fatalf("cache directory was reused across invocations: %q", cacheHome)
	}

	cleanup()
	if _, err := os.Lstat(cacheHome); !os.IsNotExist(err) {
		t.Fatalf("cache cleanup error = %v, want not exist", err)
	}
	if linkTarget, err := os.Readlink(hostilePath); err != nil || linkTarget != hostileTarget {
		t.Fatalf("hostile path changed: target=%q error=%v", linkTarget, err)
	}
}

func TestSafeEnvironmentRejectsUnusableTempDir(t *testing.T) {
	originalEnviron := environ
	t.Cleanup(func() { environ = originalEnviron })
	tempFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(tempFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tempFile)
	environ = func() []string { return []string{"HOME=/home/mk", "PATH=/usr/bin:/bin"} }

	_, cleanup, err := safeEnvironment()
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("safe environment accepted an unusable temp directory")
	}
}

func TestCodexJudgeIsReadOnlyAndSchemaDirected(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "judgment.json")
	if err := os.WriteFile(output, []byte(noOpJudgmentJSON()), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	backend := Codex{Binary: "codex", Model: "gpt-5.4", Runner: runner}
	request := JudgmentRequest{
		WorkingDir:    "/repo",
		SchemaPath:    "/schemas/judgment.json",
		OutputPath:    output,
		Observation:   []byte(`{"beads":[]}`),
		MaxInputBytes: 4096,
	}

	judgment, meta, err := backend.Judge(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if judgment.NoOpReason == "" || meta.Backend != "codex" || meta.Model != "gpt-5.4" {
		t.Fatalf("judgment/meta = %#v %#v", judgment, meta)
	}
	want := []string{
		"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--sandbox=read-only", "--cd=/repo", "--model=gpt-5.4",
		"--output-schema=/schemas/judgment.json", "--output-last-message=" + output,
		"--color=never", "--json", "-",
	}
	if got := runner.calls[0].Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	prompt := string(runner.calls[0].Stdin)
	for _, required := range []string{
		"UNTRUSTED CANONICAL DATA",
		"one selected P4",
		`kind "discovery": id is a discovery ID`,
		`Never use plural artifact names such as "discoveries" or "beads" as evidence kinds.`,
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("judge prompt missing %q: %s", required, prompt)
		}
	}
}

func TestCodexExecuteIsWorkspaceBoundAndDisablesToolNetwork(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "execution.json")
	if err := os.WriteFile(output, []byte(executionReportJSON()), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	backend := Codex{Binary: "codex", Model: "gpt-5.4", Runner: runner}
	report, _, err := backend.Execute(context.Background(), ExecutionRequest{
		Worktree:   "/worktree",
		SchemaPath: "/schemas/execution.json",
		OutputPath: output,
		Contract:   harnessContract(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Completed {
		t.Fatal("execution report was not parsed")
	}
	want := []string{
		"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--sandbox=workspace-write", "--cd=/worktree", "--model=gpt-5.4",
		"--config", `sandbox_workspace_write.network_access=false`,
		"--config", `approval_policy="never"`,
		"--output-schema=/schemas/execution.json", "--output-last-message=" + output,
		"--color=never", "--json", "-",
	}
	if got := runner.calls[0].Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	prompt := string(runner.calls[0].Stdin)
	for _, required := range []string{"NEVER push", "Allowed paths", "internal/roadmap", "immutable evidence contract"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("execution prompt missing %q: %s", required, prompt)
		}
	}
}

func TestExecutionReportMustDeclareCompletion(t *testing.T) {
	report := ExecutionReport{
		SchemaVersion: ExecutionSchemaV1,
		Summary:       "Stopped before the bounded implementation was complete.",
		ChangedPaths:  []string{"internal/roadmap/cache_test.go"},
	}
	if err := validateExecutionReport(report, harnessContract()); err == nil || !strings.Contains(err.Error(), "completed") {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeJudgeUsesReadOnlyToolsAndParsesStructuredEnvelope(t *testing.T) {
	schema := `{"type":"object"}`
	envelope := map[string]any{
		"type":              "result",
		"subtype":           "success",
		"structured_output": json.RawMessage(noOpJudgmentJSON()),
	}
	stdout, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{responses: []adapters.Result{{Stdout: stdout}}}
	backend := Claude{Binary: "claude", Model: "sonnet", Runner: runner}
	judgment, meta, err := backend.Judge(context.Background(), JudgmentRequest{
		WorkingDir:    "/repo",
		SchemaJSON:    []byte(schema),
		Observation:   []byte(`{"beads":[]}`),
		MaxInputBytes: 4096,
		MaxBudgetUSD:  1.25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if judgment.NoOpReason == "" || meta.Backend != "claude" {
		t.Fatalf("judgment/meta = %#v %#v", judgment, meta)
	}
	want := []string{
		"-p", "--safe-mode", "--no-session-persistence", "--disable-slash-commands", "--no-chrome",
		"--permission-mode=dontAsk", "--tools=Read,Glob,Grep", "--allowedTools=Read,Glob,Grep",
		"--disallowedTools=Bash,Edit,Write,WebFetch,WebSearch,NotebookEdit",
		"--model=sonnet", "--max-budget-usd=1.25", "--json-schema=" + schema, "--output-format=json",
	}
	if got := runner.calls[0].Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestClaudeExecuteAllowsOnlyNarrowBenchmarkShell(t *testing.T) {
	envelope := map[string]any{
		"type":              "result",
		"subtype":           "success",
		"structured_output": json.RawMessage(executionReportJSON()),
		"num_turns":         4,
		"total_cost_usd":    0.42,
	}
	stdout, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{responses: []adapters.Result{{Stdout: stdout}}}
	backend := Claude{Binary: "claude", Model: "sonnet", Runner: runner}
	report, meta, err := backend.Execute(context.Background(), ExecutionRequest{
		Worktree:   "/worktree",
		SchemaJSON: []byte(`{"type":"object"}`),
		Contract:   harnessContract(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Completed {
		t.Fatal("execution report was not parsed")
	}
	if meta.Turns != 4 || meta.CostUSD != 0.42 {
		t.Fatalf("usage metadata = %#v", meta)
	}
	args := runner.calls[0].Args
	if !containsArg(args, "--safe-mode") {
		t.Fatalf("Claude execution did not disable inherited customizations: %#v", args)
	}
	allowed := findPrefix(args, "--allowedTools=")
	if !strings.Contains(allowed, "Bash(go *)") || strings.Contains(allowed, "Bash(git *)") || strings.Contains(allowed, "Bash(*)") {
		t.Fatalf("unsafe Claude allowed tools: %s", allowed)
	}
	disallowed := findPrefix(args, "--disallowedTools=")
	for _, tool := range []string{"WebFetch", "WebSearch"} {
		if !strings.Contains(disallowed, tool) {
			t.Fatalf("missing disallowed tool %s: %s", tool, disallowed)
		}
	}
}

func TestClaudeReviewUsesSelfContainedEvidenceAndParsesLoneJSONFence(t *testing.T) {
	envelope := map[string]any{
		"type":      "result",
		"subtype":   "success",
		"result":    "```json\n" + reviewJSON() + "\n```",
		"num_turns": 1,
	}
	stdout, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{responses: []adapters.Result{{Stdout: stdout}}}
	backend := Claude{Binary: "claude", Model: "opus", Runner: runner}
	review, _, err := backend.Review(context.Background(), ReviewRequest{
		WorkingDir:    "/worktree",
		SchemaJSON:    []byte(`{"type":"object"}`),
		Contract:      harnessContract(),
		ContractHash:  strings.Repeat("a", 64),
		Material:      []byte(`{"artifacts":[{"kind":"measurement","path":"/cycle/measurement.json","digest":"` + strings.Repeat("b", 64) + `"}]}`),
		MaxInputBytes: 4096,
		MaxBudgetUSD:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict != domain.VerdictCloseSuccess {
		t.Fatalf("review = %#v", review)
	}
	args := runner.calls[0].Args
	if !containsArg(args, "--tools=") || !containsArg(args, "--allowedTools=") {
		t.Fatalf("Claude review did not disable all tools: %#v", args)
	}
	disallowed := findPrefix(args, "--disallowedTools=")
	for _, tool := range []string{"Read", "Glob", "Grep", "Bash", "Edit", "Write", "WebFetch", "WebSearch"} {
		if !strings.Contains(disallowed, tool) {
			t.Fatalf("Claude review did not disallow %s: %s", tool, disallowed)
		}
	}
	prompt := string(runner.calls[0].Stdin)
	for _, required := range []string{
		"self-contained evidence package",
		"Remontoire, not you, executed the benchmark",
		"content-addressed citation keys, not signatures",
		"Set id to that artifact path basename",
		"including when your verdict is inconclusive",
		"Required top-level object with exactly these five fields",
		`"schema_version":"remontoire.review/v1"`,
		`"contract_hash":"` + strings.Repeat("a", 64) + `"`,
		`"digest":"` + strings.Repeat("0", 64) + `"`,
		"Do not substitute summary for rationale or add checks",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("review prompt missing %q: %s", required, prompt)
		}
	}
}

func TestClaudeReviewRepairsInvalidSchemaOnceWithinRemainingBudget(t *testing.T) {
	invalidReview := `{"contract_hash":"` + strings.Repeat("a", 64) + `","verdict":"close_success","rationale":"The bounded evidence is internally consistent.","evidence":[{"kind":"measurement","id":"measurement.json","digest":"` + strings.Repeat("b", 64) + `"}]}`
	invalidEnvelope, err := json.Marshal(map[string]any{
		"type":           "result",
		"subtype":        "success",
		"result":         "```json\n" + invalidReview + "\n```",
		"num_turns":      1,
		"total_cost_usd": 0.2,
	})
	if err != nil {
		t.Fatal(err)
	}
	validEnvelope, err := json.Marshal(map[string]any{
		"type":           "result",
		"subtype":        "success",
		"result":         "```json\n" + reviewJSON() + "\n```",
		"num_turns":      1,
		"total_cost_usd": 0.3,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{responses: []adapters.Result{{Stdout: invalidEnvelope}, {Stdout: validEnvelope}}}
	backend := Claude{Binary: "claude", Model: "sonnet", Runner: runner}

	review, meta, err := backend.Review(context.Background(), ReviewRequest{
		WorkingDir:    "/worktree",
		SchemaJSON:    []byte(`{"type":"object"}`),
		Contract:      harnessContract(),
		ContractHash:  strings.Repeat("a", 64),
		Material:      []byte(`{"artifacts":[{"kind":"measurement","path":"/cycle/measurement.json","digest":"` + strings.Repeat("b", 64) + `"}]}`),
		MaxInputBytes: 4096,
		MaxBudgetUSD:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict != domain.VerdictCloseSuccess {
		t.Fatalf("review = %#v", review)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("Claude calls = %d, want one initial attempt and one repair", len(runner.calls))
	}
	if got := findPrefix(runner.calls[1].Args, "--max-budget-usd="); got != "--max-budget-usd=0.8" {
		t.Fatalf("repair budget = %q, want remaining budget", got)
	}
	if prompt := string(runner.calls[1].Stdin); !strings.Contains(prompt, "previous response failed deterministic schema validation") {
		t.Fatalf("repair prompt does not explain the retry boundary: %s", prompt)
	}
	if meta.Turns != 2 || meta.CostUSD != 0.5 {
		t.Fatalf("usage metadata = %#v", meta)
	}
	if !bytes.Contains(meta.Transcript, invalidEnvelope) || !bytes.Contains(meta.Transcript, validEnvelope) {
		t.Fatalf("combined transcript omitted an attempt: %s", meta.Transcript)
	}
}

func TestDecodeClaudeRejectsFencedJSONSurroundedByProse(t *testing.T) {
	stdout, err := json.Marshal(map[string]any{
		"type":    "result",
		"subtype": "success",
		"result":  "Review complete.\n```json\n" + reviewJSON() + "\n```",
	})
	if err != nil {
		t.Fatal(err)
	}
	var review domain.Review
	if err := decodeClaude(stdout, &review); err == nil {
		t.Fatal("Claude prose-wrapped fenced JSON was accepted")
	}
}

func TestDecodeClaudeRejectsUnknownFieldsInFencedJSON(t *testing.T) {
	payload := strings.Replace(reviewJSON(), `"contract_hash"`, `"unexpected":true,"contract_hash"`, 1)
	stdout, err := json.Marshal(map[string]any{
		"type":    "result",
		"subtype": "success",
		"result":  "```json\n" + payload + "\n```",
	})
	if err != nil {
		t.Fatal(err)
	}
	var review domain.Review
	if err := decodeClaude(stdout, &review); err == nil || !strings.Contains(err.Error(), "exact field") {
		t.Fatalf("error = %v, want inexact field rejection", err)
	}
}

func TestDecodeClaudeRejectsDuplicateFieldsInFencedJSON(t *testing.T) {
	payload := strings.Replace(reviewJSON(), `"contract_hash"`, `"contract_hash":"`+strings.Repeat("c", 64)+`","contract_hash"`, 1)
	stdout, err := json.Marshal(map[string]any{
		"type":    "result",
		"subtype": "success",
		"result":  "```json\n" + payload + "\n```",
	})
	if err != nil {
		t.Fatal(err)
	}
	var review domain.Review
	if err := decodeClaude(stdout, &review); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v, want duplicate field rejection", err)
	}
}

func TestDecodeClaudeRejectsCaseVariantFieldName(t *testing.T) {
	payload := strings.Replace(reviewJSON(), `"schema_version"`, `"SCHEMA_VERSION"`, 1)
	stdout, err := json.Marshal(map[string]any{
		"type":    "result",
		"subtype": "success",
		"result":  "```json\n" + payload + "\n```",
	})
	if err != nil {
		t.Fatal(err)
	}
	var review domain.Review
	if err := decodeClaude(stdout, &review); err == nil || !strings.Contains(err.Error(), "exact field") {
		t.Fatalf("error = %v, want exact field rejection", err)
	}
}

func TestDecodeClaudeRejectsCaseVariantDuplicateField(t *testing.T) {
	payload := strings.Replace(reviewJSON(), `"verdict":"close_success"`, `"verdict":"close_success","VERDICT":"close_failure"`, 1)
	stdout, err := json.Marshal(map[string]any{
		"type":    "result",
		"subtype": "success",
		"result":  "```json\n" + payload + "\n```",
	})
	if err != nil {
		t.Fatal(err)
	}
	var review domain.Review
	if err := decodeClaude(stdout, &review); err == nil || !strings.Contains(err.Error(), "exact field") {
		t.Fatalf("error = %v, want case-variant duplicate rejection", err)
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

func findPrefix(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return arg
		}
	}
	return ""
}
