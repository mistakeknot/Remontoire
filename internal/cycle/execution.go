package cycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

var (
	ErrExecutionIndeterminate = errors.New("execution attempt is indeterminate and will not be repeated")
	ErrBenchmarkIndeterminate = errors.New("benchmark attempt is indeterminate and will not be repeated")
)

func (s *Service) Approve(ctx context.Context, cycleID, actor string) (cycle domain.Cycle, err error) {
	if strings.TrimSpace(actor) == "" || strings.EqualFold(strings.TrimSpace(actor), "remontoire") {
		return domain.Cycle{}, fmt.Errorf("approval actor must identify an external principal")
	}
	if err := s.validate(); err != nil {
		return domain.Cycle{}, err
	}
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	owner := "remontoire:" + cycleID
	if err := s.Kernel.AcquireCycleLock(ctx, cycle.Portfolio, owner, s.Config.LockTimeout); err != nil {
		return domain.Cycle{}, err
	}
	defer func() {
		cleanupCtx, cancel := boundedCleanupContext(ctx)
		defer cancel()
		releaseErr := s.Kernel.ReleaseCycleLock(cleanupCtx, cycle.Portfolio, owner)
		if err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	if err := s.ensureStageEvent(ctx, &cycle); err != nil {
		return cycle, err
	}
	if cycle.Stage == domain.StageApproved && cycle.Approval != nil {
		return cycle, nil
	}
	if cycle.Stage != domain.StageAwaitingApproval || cycle.Candidate == nil {
		return cycle, fmt.Errorf("cycle %s is not awaiting approval", cycle.ID)
	}
	approval := domain.Approval{
		SchemaVersion: domain.ApprovalSchemaV1,
		CycleID:       cycle.ID,
		ContractHash:  cycle.ContractHash,
		Actor:         strings.TrimSpace(actor),
		ApprovedAt:    s.now(),
	}
	if err := domain.ValidateApproval(approval, cycle.ID, cycle.Candidate.Contract); err != nil {
		return cycle, err
	}
	artifact, err := s.Store.WriteJSON(cycle.ID, "approval", "approval.json", approval)
	if err != nil {
		return cycle, err
	}
	cycle.Approval = &approval
	cycle.Artifacts = append(cycle.Artifacts, artifact)
	cycle.IdempotencyKeys["approval"] = cycle.ContractHash
	if err := s.transition(ctx, &cycle, domain.StageApproved); err != nil {
		return cycle, err
	}
	return cycle, nil
}

func (s *Service) Execute(ctx context.Context, cycleID string) (cycle domain.Cycle, err error) {
	if err := s.validateExecution(); err != nil {
		return domain.Cycle{}, err
	}
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	if cycle.Stage != domain.StageApproved && cycle.Stage != domain.StageExecuting && !(cycle.Stage == domain.StageReviewing && cycle.IdempotencyKeys["benchmark:attempt"] == "completed") {
		return cycle, fmt.Errorf("cycle %s must be approved before execution (stage %s)", cycle.ID, cycle.Stage)
	}
	owner := "remontoire:" + cycleID
	if err := s.Kernel.AcquireCycleLock(ctx, cycle.Portfolio, owner, s.Config.LockTimeout); err != nil {
		return domain.Cycle{}, err
	}
	defer func() {
		cleanupCtx, cancel := boundedCleanupContext(ctx)
		defer cancel()
		releaseErr := s.Kernel.ReleaseCycleLock(cleanupCtx, cycle.Portfolio, owner)
		if err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	if err := s.ensureStageEvent(ctx, &cycle); err != nil {
		return cycle, err
	}
	if cycle.Stage == domain.StageReviewing && cycle.IdempotencyKeys["benchmark:attempt"] == "completed" {
		return cycle, nil
	}
	if cycle.Candidate == nil || cycle.Approval == nil {
		return cycle, fmt.Errorf("cycle %s has no approved candidate", cycle.ID)
	}
	if err := domain.ValidateApproval(*cycle.Approval, cycle.ID, cycle.Candidate.Contract); err != nil {
		return cycle, err
	}
	if cycle.ContractHash != cycle.Approval.ContractHash {
		return cycle, fmt.Errorf("contract_hash no longer matches approval")
	}

	backendName := strings.ToLower(cycle.Candidate.Contract.Executor)
	executor, ok := s.Executors[backendName]
	if !ok {
		return cycle, fmt.Errorf("executor backend %q is not configured", backendName)
	}
	outputPath, err := s.Store.Path(cycle.ID, "execution-report.json")
	if err != nil {
		return cycle, err
	}

	var report harness.ExecutionReport
	var metadata harness.Metadata
	executionCompleteAtStart := cycle.IdempotencyKeys["execution:attempt"] == "completed"
	if cycle.IdempotencyKeys["execution:attempt"] != "started" && cycle.IdempotencyKeys["execution:attempt"] != "completed" {
		if cycle.Execution == nil {
			repository, resolveErr := s.resolveRepository(cycle.Candidate.Contract.Repository)
			if resolveErr != nil {
				return cycle, s.fail(ctx, &cycle, resolveErr)
			}
			info, prepareErr := s.Worktrees.Prepare(ctx, repository, cycle.ID)
			if prepareErr != nil {
				return cycle, s.fail(ctx, &cycle, fmt.Errorf("prepare worktree: %w", prepareErr))
			}
			cycle.Execution = &domain.ExecutionRecord{
				Backend: backendName, WorktreePath: info.Path, BaseCommit: info.BaseCommit, StartedAt: s.now(),
			}
		}
		if cycle.IdempotencyKeys["execution:prepared"] != "completed" {
			cycle.IdempotencyKeys["execution:prepared"] = "completed"
			if err := s.persist(ctx, &cycle); err != nil {
				return cycle, err
			}
		}
	}

	if cycle.Stage == domain.StageApproved || cycle.Stage == domain.StageExecuting {
		if err := s.advanceOnce(ctx, &cycle, "run:execute", "propose", "execute"); err != nil {
			return cycle, err
		}
		if cycle.Stage == domain.StageApproved {
			if err := s.transition(ctx, &cycle, domain.StageExecuting); err != nil {
				return cycle, err
			}
		}
	}

	invokeExecution := false
	if cycle.IdempotencyKeys["execution:attempt"] != "started" && cycle.IdempotencyKeys["execution:attempt"] != "completed" {
		cycle.IdempotencyKeys["execution:attempt"] = "started"
		if err := s.persist(ctx, &cycle); err != nil {
			return cycle, err
		}
		invokeExecution = true
	}

	if cycle.IdempotencyKeys["execution:attempt"] == "started" && !invokeExecution {
		if cycle.Execution == nil {
			return cycle, ErrExecutionIndeterminate
		}
		if _, statErr := os.Stat(outputPath); statErr != nil {
			if os.IsNotExist(statErr) {
				return cycle, ErrExecutionIndeterminate
			}
			return cycle, statErr
		}
		report, err = harness.LoadExecutionReport(outputPath, cycle.Candidate.Contract)
		if err != nil {
			return cycle, fmt.Errorf("recover execution report: %w", err)
		}
		reportArtifact, hashErr := s.Store.HashExisting("execution-report", outputPath)
		if hashErr != nil {
			return cycle, hashErr
		}
		cycle.Artifacts = append(cycle.Artifacts, reportArtifact)
		metadata = harness.Metadata{Backend: cycle.Execution.Backend, Model: cycle.Execution.Model, Turns: cycle.Execution.Turns, CostUSD: cycle.Execution.CostUSD}
	} else if invokeExecution {
		executionCtx, cancel := context.WithTimeout(ctx, time.Duration(cycle.Candidate.Contract.Budget.MaxDurationSeconds)*time.Second)
		report, metadata, err = executor.Execute(executionCtx, harness.ExecutionRequest{
			Worktree: cycle.Execution.WorktreePath, SchemaPath: s.Config.ExecutionSchemaPath, OutputPath: outputPath, Contract: cycle.Candidate.Contract,
		})
		cancel()
		if err != nil {
			return cycle, s.fail(ctx, &cycle, fmt.Errorf("bounded executor: %w", err))
		}
		reportArtifact, writeErr := s.Store.WriteJSON(cycle.ID, "execution-report", "execution-report.json", report)
		if writeErr != nil {
			return cycle, s.fail(ctx, &cycle, writeErr)
		}
		cycle.Artifacts = append(cycle.Artifacts, reportArtifact)
	}

	if !executionCompleteAtStart && metadata.Turns > cycle.Candidate.Contract.Budget.MaxTurns {
		return cycle, s.fail(ctx, &cycle, fmt.Errorf("executor used %d turns, budget is %d", metadata.Turns, cycle.Candidate.Contract.Budget.MaxTurns))
	}
	if !executionCompleteAtStart && metadata.CostUSD > cycle.Candidate.Contract.Budget.MaxCostUSD {
		return cycle, s.fail(ctx, &cycle, fmt.Errorf("executor cost %.2f exceeds budget %.2f", metadata.CostUSD, cycle.Candidate.Contract.Budget.MaxCostUSD))
	}
	if cycle.Execution == nil {
		return cycle, s.fail(ctx, &cycle, fmt.Errorf("execution record is missing"))
	}
	if !executionCompleteAtStart {
		changed, err := s.Worktrees.ChangedPaths(ctx, cycle.Execution.WorktreePath)
		if err != nil {
			return cycle, s.fail(ctx, &cycle, fmt.Errorf("inspect execution paths: %w", err))
		}
		if err := adapters.CheckAllowedPaths(changed, cycle.Candidate.Contract.AllowedPaths); err != nil {
			return cycle, s.fail(ctx, &cycle, err)
		}
		if !samePaths(changed, report.ChangedPaths) {
			return cycle, s.fail(ctx, &cycle, fmt.Errorf("execution report changed_paths do not match git state"))
		}
		patch, err := s.Worktrees.Patch(ctx, cycle.Execution.WorktreePath, changed)
		if err != nil {
			return cycle, s.fail(ctx, &cycle, err)
		}
		patchArtifact, err := s.Store.WriteBytes(cycle.ID, "patch", "experiment.patch", patch)
		if err != nil {
			return cycle, s.fail(ctx, &cycle, err)
		}
		cycle.Artifacts = append(cycle.Artifacts, patchArtifact)
		if len(metadata.Transcript) > 0 {
			artifact, writeErr := s.Store.WriteBytes(cycle.ID, "executor-transcript", "executor.jsonl", metadata.Transcript)
			if writeErr != nil {
				return cycle, s.fail(ctx, &cycle, writeErr)
			}
			cycle.Artifacts = append(cycle.Artifacts, artifact)
		}
		if len(metadata.Stderr) > 0 {
			artifact, writeErr := s.Store.WriteBytes(cycle.ID, "executor-stderr", "executor.stderr", metadata.Stderr)
			if writeErr != nil {
				return cycle, s.fail(ctx, &cycle, writeErr)
			}
			cycle.Artifacts = append(cycle.Artifacts, artifact)
		}
		cycle.Execution.Backend = metadata.Backend
		cycle.Execution.Model = metadata.Model
		cycle.Execution.Turns = metadata.Turns
		cycle.Execution.CostUSD = metadata.CostUSD
		cycle.Execution.ChangedPaths = changed
		cycle.Execution.CompletedAt = s.now()
		cycle.IdempotencyKeys["execution:attempt"] = "completed"
		if err := s.persist(ctx, &cycle); err != nil {
			return cycle, err
		}
	}

	if cycle.IdempotencyKeys["benchmark:attempt"] == "started" {
		var measurement domain.Measurement
		if err := s.Store.ReadJSON(cycle.ID, "measurement.json", &measurement); err != nil {
			if os.IsNotExist(err) {
				return cycle, ErrBenchmarkIndeterminate
			}
			return cycle, err
		}
		cycle.Measurement = &measurement
		if err := s.restoreMeasurementArtifacts(&cycle, measurement); err != nil {
			return cycle, err
		}
	} else if cycle.IdempotencyKeys["benchmark:attempt"] != "completed" {
		cycle.IdempotencyKeys["benchmark:attempt"] = "started"
		if err := s.persist(ctx, &cycle); err != nil {
			return cycle, err
		}
		measurement, artifacts, measureErr := s.measure(ctx, cycle)
		if measureErr != nil {
			return cycle, s.fail(ctx, &cycle, measureErr)
		}
		cycle.Measurement = &measurement
		cycle.Artifacts = append(cycle.Artifacts, artifacts...)
		measurementArtifact, err := s.Store.WriteJSON(cycle.ID, "measurement", "measurement.json", measurement)
		if err != nil {
			return cycle, s.fail(ctx, &cycle, err)
		}
		cycle.Artifacts = append(cycle.Artifacts, measurementArtifact)
	}
	cycle.IdempotencyKeys["benchmark:attempt"] = "completed"
	if err := s.persist(ctx, &cycle); err != nil {
		return cycle, err
	}
	if err := s.advanceOnce(ctx, &cycle, "run:review", "execute", "review"); err != nil {
		return cycle, err
	}
	if err := s.transition(ctx, &cycle, domain.StageReviewing); err != nil {
		return cycle, err
	}
	return cycle, nil
}

func (s *Service) validateExecution() error {
	if err := s.validate(); err != nil {
		return err
	}
	if len(s.Executors) == 0 || s.Worktrees == nil || s.BenchmarkRunner == nil {
		return fmt.Errorf("executors, worktree manager, and benchmark runner are required")
	}
	if !filepath.IsAbs(s.Config.WorktreeRoot) || s.Config.ExecutionSchemaPath == "" {
		return fmt.Errorf("absolute worktree root and execution schema path are required")
	}
	return nil
}

func (s *Service) measure(ctx context.Context, cycle domain.Cycle) (domain.Measurement, []domain.Artifact, error) {
	contract := cycle.Candidate.Contract
	benchmarkCtx, cancel := context.WithTimeout(ctx, time.Duration(contract.Budget.MaxDurationSeconds)*time.Second)
	defer cancel()
	started := time.Now()
	result, runErr := s.BenchmarkRunner.Run(benchmarkCtx, adapters.Invocation{
		Name: contract.Benchmark[0], Args: contract.Benchmark[1:], Dir: cycle.Execution.WorktreePath,
		Env: harness.SafeEnvironment(), MaxOutputBytes: 8 << 20,
	})
	duration := time.Since(started)
	if runErr != nil && len(result.Stderr) == 0 {
		result.Stderr = []byte(runErr.Error())
	}
	stdoutArtifact, err := s.Store.WriteBytes(cycle.ID, "benchmark-stdout", "benchmark.stdout", result.Stdout)
	if err != nil {
		return domain.Measurement{}, nil, err
	}
	stderrArtifact, err := s.Store.WriteBytes(cycle.ID, "benchmark-stderr", "benchmark.stderr", result.Stderr)
	if err != nil {
		return domain.Measurement{}, nil, err
	}
	value, extractErr := ExtractMetric(contract.Metric, result.Stdout, duration)
	if extractErr != nil {
		value = 0
		if result.ExitCode == 0 {
			return domain.Measurement{}, []domain.Artifact{stdoutArtifact, stderrArtifact}, extractErr
		}
	}
	measurement := domain.Measurement{
		MetricName: contract.Metric.Name, Value: value, ExitCode: result.ExitCode, Duration: duration,
		StdoutPath: stdoutArtifact.Path, StderrPath: stderrArtifact.Path,
	}
	return measurement, []domain.Artifact{stdoutArtifact, stderrArtifact}, nil
}

func ExtractMetric(metric domain.Metric, stdout []byte, duration time.Duration) (float64, error) {
	switch metric.Source {
	case domain.MetricSourceWallDurationMS:
		return float64(duration.Microseconds()) / 1000, nil
	case domain.MetricSourceStdoutJSON:
		var value any
		if err := json.Unmarshal(stdout, &value); err != nil {
			return 0, fmt.Errorf("benchmark stdout is not JSON: %w", err)
		}
		current := value
		for _, segment := range strings.Split(metric.JSONField, ".") {
			object, ok := current.(map[string]any)
			if !ok {
				return 0, fmt.Errorf("metric json_field %q is not an object path", metric.JSONField)
			}
			current, ok = object[segment]
			if !ok {
				return 0, fmt.Errorf("metric json_field %q is missing", metric.JSONField)
			}
		}
		number, ok := current.(float64)
		if !ok {
			return 0, fmt.Errorf("metric json_field %q is not numeric", metric.JSONField)
		}
		return number, nil
	default:
		return 0, fmt.Errorf("unsupported metric source %q", metric.Source)
	}
}

func samePaths(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if filepath.ToSlash(filepath.Clean(a[i])) != filepath.ToSlash(filepath.Clean(b[i])) {
			return false
		}
	}
	return true
}

func (s *Service) restoreMeasurementArtifacts(cycle *domain.Cycle, measurement domain.Measurement) error {
	measurementPath, err := s.Store.Path(cycle.ID, "measurement.json")
	if err != nil {
		return err
	}
	for _, item := range []struct {
		kind string
		path string
	}{
		{kind: "measurement", path: measurementPath},
		{kind: "benchmark-stdout", path: measurement.StdoutPath},
		{kind: "benchmark-stderr", path: measurement.StderrPath},
	} {
		if item.path == "" {
			continue
		}
		artifact, err := s.Store.HashExisting(item.kind, item.path)
		if err != nil {
			return err
		}
		cycle.Artifacts = append(cycle.Artifacts, artifact)
	}
	return nil
}
