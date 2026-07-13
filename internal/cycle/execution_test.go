package cycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

type fakeExecutor struct {
	calls  int
	report harness.ExecutionReport
	meta   harness.Metadata
	err    error
	check  func()
}

func (e *fakeExecutor) Execute(_ context.Context, _ harness.ExecutionRequest) (harness.ExecutionReport, harness.Metadata, error) {
	e.calls++
	if e.check != nil {
		e.check()
	}
	return e.report, e.meta, e.err
}

type fakeWorktrees struct {
	info       adapters.WorktreeInfo
	paths      []string
	patch      []byte
	prepare    int
	repository string
	changed    int
	patched    int
}

func (w *fakeWorktrees) Prepare(_ context.Context, repository, _ string) (adapters.WorktreeInfo, error) {
	w.prepare++
	w.repository = repository
	return w.info, nil
}
func (w *fakeWorktrees) ChangedPaths(context.Context, string) ([]string, error) {
	w.changed++
	return append([]string(nil), w.paths...), nil
}
func (w *fakeWorktrees) Patch(context.Context, string, []string) ([]byte, error) {
	w.patched++
	return append([]byte(nil), w.patch...), nil
}

type benchmarkRunner struct {
	calls  int
	result adapters.Result
	err    error
}

func (r *benchmarkRunner) Run(context.Context, adapters.Invocation) (adapters.Result, error) {
	r.calls++
	return r.result, r.err
}

func executionService(t *testing.T) (*Service, *fakeKernel, *fakeBacklog, *fakeExecutor, *fakeWorktrees, *benchmarkRunner) {
	t.Helper()
	service, kernel, backlog := testService(t, domain.ModeProposal)
	worktreePath := filepath.Join(t.TempDir(), "cycle-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{
		report: harness.ExecutionReport{
			SchemaVersion: harness.ExecutionSchemaV1,
			Summary:       "Added bounded roadmap benchmark coverage.",
			ChangedPaths:  []string{"internal/roadmap/cache_test.go"},
			Commands:      []string{"go test ./internal/roadmap"},
			Completed:     true,
		},
		meta: harness.Metadata{Backend: "codex", Model: "fixture", Transcript: []byte("transcript\n")},
	}
	worktrees := &fakeWorktrees{
		info:  adapters.WorktreeInfo{Path: worktreePath, BaseCommit: strings.Repeat("a", 40)},
		paths: []string{"internal/roadmap/cache_test.go"},
		patch: []byte("diff --git a/internal/roadmap/cache_test.go b/internal/roadmap/cache_test.go\n"),
	}
	benchmark := &benchmarkRunner{result: adapters.Result{Stdout: []byte("ok\n"), ExitCode: 0}}
	service.Executors = map[string]Executor{"codex": executor}
	service.Worktrees = worktrees
	service.BenchmarkRunner = benchmark
	service.Config.ExecutionSchemaPath = filepath.Join(t.TempDir(), "execution.json")
	service.Config.WorktreeRoot = filepath.Dir(worktreePath)
	return service, kernel, backlog, executor, worktrees, benchmark
}

func TestApproveBindsActorAndImmutableContractHash(t *testing.T) {
	service, _, _, _, _, _ := executionService(t)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.Approve(context.Background(), cycle.ID, "mk")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Stage != domain.StageApproved || approved.Approval == nil || approved.Approval.Actor != "mk" || approved.Approval.ContractHash != approved.ContractHash {
		t.Fatalf("approved cycle = %#v", approved)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "remontoire"); err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("self approval error = %v", err)
	}
}

func TestExecuteRequiresApproval(t *testing.T) {
	service, _, _, executor, _, _ := executionService(t)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Execute(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "approved") {
		t.Fatalf("error = %v", err)
	}
	if executor.calls != 0 {
		t.Fatal("executor ran without approval")
	}
}

func TestExecuteRejectsStaleApprovalHash(t *testing.T) {
	service, kernel, _, executor, _, _ := executionService(t)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	cycle, err = service.Approve(context.Background(), cycle.ID, "mk")
	if err != nil {
		t.Fatal(err)
	}
	cycle.Candidate.Contract.Hypothesis += " changed"
	kernel.cycles[cycle.ID] = cycle
	_, err = service.Execute(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "contract_hash") {
		t.Fatalf("error = %v", err)
	}
	if executor.calls != 0 {
		t.Fatal("executor ran with stale approval")
	}
}

func TestExecuteRejectsRepositorySymlinkEscapeAfterApproval(t *testing.T) {
	service, _, _, executor, worktrees, _ := executionService(t)
	link := filepath.Join(filepath.Dir(service.Config.ProjectDir), "approved-repository")
	if err := os.Symlink(service.Config.ProjectDir, link); err != nil {
		t.Fatal(err)
	}
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, link)
		return judgment
	}}
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "mk"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	_, err = service.Execute(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "outside allowed repository roots") {
		t.Fatalf("error = %v", err)
	}
	if worktrees.prepare != 0 || executor.calls != 0 {
		t.Fatalf("worktree prepares=%d executor calls=%d repository=%q", worktrees.prepare, executor.calls, worktrees.repository)
	}
}

func TestApprovedExecutionRunsOnceMeasuresAndAdvancesToReview(t *testing.T) {
	service, _, _, executor, worktrees, benchmark := executionService(t)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "mk"); err != nil {
		t.Fatal(err)
	}
	executed, err := service.Execute(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if executed.Stage != domain.StageReviewing || executed.Execution == nil || executed.Measurement == nil {
		t.Fatalf("executed cycle = %#v", executed)
	}
	if executor.calls != 1 || worktrees.prepare != 1 || benchmark.calls != 1 {
		t.Fatalf("calls executor=%d worktree=%d benchmark=%d", executor.calls, worktrees.prepare, benchmark.calls)
	}
	if executed.Measurement.MetricName != executed.Candidate.Contract.Metric.Name || executed.Measurement.ExitCode != 0 {
		t.Fatalf("measurement = %#v", executed.Measurement)
	}
	repeated, err := service.Execute(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Stage != domain.StageReviewing || executor.calls != 1 || benchmark.calls != 1 {
		t.Fatalf("repeat duplicated execution: executor=%d benchmark=%d", executor.calls, benchmark.calls)
	}
}

func TestExecutionPathViolationFailsCycle(t *testing.T) {
	service, _, _, executor, worktrees, _ := executionService(t)
	worktrees.paths = []string{"README.md"}
	executor.report.ChangedPaths = []string{"README.md"}
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "mk"); err != nil {
		t.Fatal(err)
	}
	failed, err := service.Execute(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "allowed") {
		t.Fatalf("error = %v", err)
	}
	if failed.Stage != domain.StageFailed || executor.calls != 1 {
		t.Fatalf("failed cycle=%#v executor calls=%d", failed, executor.calls)
	}
}

func TestExecutionUsageBudgetFailsBeforeBenchmark(t *testing.T) {
	service, _, _, executor, _, benchmark := executionService(t)
	executor.meta.Turns = 7
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "mk"); err != nil {
		t.Fatal(err)
	}
	failed, err := service.Execute(context.Background(), cycle.ID)
	if err == nil || !strings.Contains(err.Error(), "turns") {
		t.Fatalf("error = %v", err)
	}
	if failed.Stage != domain.StageFailed || benchmark.calls != 0 {
		t.Fatalf("failed=%#v benchmark calls=%d", failed, benchmark.calls)
	}
}

func TestBenchmarkFailureCanBeSignedAsFailedCycle(t *testing.T) {
	service, kernel, _, _, _, benchmark := executionService(t)
	service.Judge = dynamicJudge{call: func(request harness.JudgmentRequest) domain.Judgment {
		judgment := selectedJudgment(t, request, service.Config.ProjectDir)
		judgment.Opportunities[0].Contract.Metric = domain.Metric{
			Name: "throughput", Unit: "items_s", Direction: domain.DirectionMaximize,
			Source: domain.MetricSourceStdoutJSON, JSONField: "metrics.throughput", Baseline: 10, Target: 20,
		}
		return judgment
	}}
	benchmark.result = adapters.Result{Stdout: []byte("not json\n"), ExitCode: 0}
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "mk"); err != nil {
		t.Fatal(err)
	}
	failed, err := service.Execute(context.Background(), cycle.ID)
	if err == nil || failed.Stage != domain.StageFailed || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("cycle=%#v error=%v", failed, err)
	}
	signed, err := service.Compound(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Stage != domain.StageFailed || signed.SignedReceiptID == "" || kernel.receiptCalls != 1 {
		t.Fatalf("signed=%#v receipt calls=%d", signed, kernel.receiptCalls)
	}
}

func TestResumeAfterExecutionCompletionDoesNotRepeatPatchWork(t *testing.T) {
	service, kernel, _, executor, worktrees, benchmark := executionService(t)
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
	cycle.Stage = domain.StageExecuting
	kernel.cycles[cycle.ID] = cycle
	if _, err := service.Execute(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || worktrees.changed != 1 || worktrees.patched != 1 || benchmark.calls != 1 {
		t.Fatalf("resume duplicated work: executor=%d changed=%d patch=%d benchmark=%d", executor.calls, worktrees.changed, worktrees.patched, benchmark.calls)
	}
}

func TestIndeterminateStartedAttemptNeverReinvokesExecutor(t *testing.T) {
	service, kernel, _, executor, worktrees, _ := executionService(t)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	cycle, err = service.Approve(context.Background(), cycle.ID, "mk")
	if err != nil {
		t.Fatal(err)
	}
	cycle.Stage = domain.StageExecuting
	cycle.Execution = &domain.ExecutionRecord{Backend: "codex", WorktreePath: worktrees.info.Path, BaseCommit: worktrees.info.BaseCommit}
	cycle.IdempotencyKeys["execution:attempt"] = "started"
	cycle.IdempotencyKeys["run:execute"] = "completed"
	kernel.phase = "execute"
	kernel.cycles[cycle.ID] = cycle

	_, err = service.Execute(context.Background(), cycle.ID)
	if !errors.Is(err, ErrExecutionIndeterminate) {
		t.Fatalf("error = %v, want ErrExecutionIndeterminate", err)
	}
	if executor.calls != 0 {
		t.Fatal("indeterminate execution was invoked a second time")
	}
}

func TestExecutionPreparationPrecedesAdvanceAndAttemptPrecedesHarness(t *testing.T) {
	service, kernel, _, executor, _, _ := executionService(t)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "mk"); err != nil {
		t.Fatal(err)
	}
	preparationWasDurable := false
	kernel.onAdvance = func(k *fakeKernel) {
		if k.phase == "propose" {
			persisted := k.cycles[cycle.ID]
			preparationWasDurable = persisted.IdempotencyKeys["execution:prepared"] == "completed" && persisted.IdempotencyKeys["execution:attempt"] == ""
		}
	}
	attemptWasDurable := false
	executor.check = func() {
		attemptWasDurable = kernel.cycles[cycle.ID].IdempotencyKeys["execution:attempt"] == "started"
	}

	if _, err := service.Execute(context.Background(), cycle.ID); err != nil {
		t.Fatal(err)
	}
	if !preparationWasDurable || !attemptWasDurable || executor.calls != 1 {
		t.Fatalf("preparation durable=%t attempt durable=%t executor calls=%d", preparationWasDurable, attemptWasDurable, executor.calls)
	}
}

func TestExecutionStageInterruptionResumesBeforeFirstExecutorCall(t *testing.T) {
	service, kernel, _, executor, _, _ := executionService(t)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "mk"); err != nil {
		t.Fatal(err)
	}
	kernel.eventErrors[domain.StageExecuting] = errors.New("stage event unavailable")

	interrupted, err := service.Execute(context.Background(), cycle.ID)
	if err == nil || interrupted.Stage != domain.StageExecuting || executor.calls != 0 {
		t.Fatalf("cycle=%#v error=%v executor=%d", interrupted, err, executor.calls)
	}
	if interrupted.IdempotencyKeys["execution:prepared"] != "completed" || interrupted.IdempotencyKeys["execution:attempt"] != "" || interrupted.IdempotencyKeys["run:execute"] != "completed" {
		t.Fatalf("keys=%#v", interrupted.IdempotencyKeys)
	}
	delete(kernel.eventErrors, domain.StageExecuting)

	resumed, err := service.Execute(context.Background(), cycle.ID)
	if err != nil || resumed.Stage != domain.StageReviewing || executor.calls != 1 {
		t.Fatalf("cycle=%#v error=%v executor=%d", resumed, err, executor.calls)
	}
}

func TestReviewAdvanceTimeoutAfterCommitIsReconciled(t *testing.T) {
	service, kernel, _, executor, _, benchmark := executionService(t)
	cycle, err := service.Start(context.Background(), domain.ModeProposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), cycle.ID, "mk"); err != nil {
		t.Fatal(err)
	}
	kernel.advanceErrAt = 4
	kernel.advanceCommitErr = true
	kernel.advanceErr = errors.New("review response timed out")

	executed, err := service.Execute(context.Background(), cycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if executed.Stage != domain.StageReviewing || executed.IdempotencyKeys["run:review"] != "completed" || kernel.phase != "review" || executor.calls != 1 || benchmark.calls != 1 {
		t.Fatalf("cycle=%#v phase=%s executor=%d benchmark=%d", executed, kernel.phase, executor.calls, benchmark.calls)
	}
}

func TestStdoutJSONMetricExtraction(t *testing.T) {
	metric := domain.Metric{Name: "throughput", Unit: "items_s", Direction: domain.DirectionMaximize, Source: domain.MetricSourceStdoutJSON, JSONField: "metrics.throughput", Baseline: 10, Target: 20}
	value, err := ExtractMetric(metric, []byte(`{"metrics":{"throughput":25.5}}`), 0)
	if err != nil || value != 25.5 {
		t.Fatalf("value=%v error=%v", value, err)
	}
	if _, err := ExtractMetric(metric, []byte(`{"metrics":{"other":1}}`), 0); err == nil {
		t.Fatal("missing JSON field was accepted")
	}
}
