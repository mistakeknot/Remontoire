package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

type Codex struct {
	Binary string
	Model  string
	Runner adapters.Runner
}

func (c Codex) Name() string { return "codex" }

func (c Codex) Judge(ctx context.Context, request JudgmentRequest) (domain.Judgment, Metadata, error) {
	sanitized, err := SanitizeObservation(request.Observation, request.MaxInputBytes)
	if err != nil {
		return domain.Judgment{}, Metadata{}, err
	}
	args := c.baseArgs("read-only", request.WorkingDir)
	args = append(args,
		"--output-schema="+request.SchemaPath,
		"--output-last-message="+request.OutputPath,
		"--color=never", "--json", "-",
	)
	result, err := c.run(ctx, args, []byte(judgmentPrompt(sanitized)))
	meta := Metadata{Backend: c.Name(), Model: c.Model, Transcript: result.Stdout, Stderr: result.Stderr}
	if err != nil {
		return domain.Judgment{}, meta, err
	}
	var judgment domain.Judgment
	if err := decodeFile(request.OutputPath, &judgment); err != nil {
		return domain.Judgment{}, meta, fmt.Errorf("codex judgment: %w", err)
	}
	if err := domain.ValidateJudgment(judgment); err != nil {
		return domain.Judgment{}, meta, fmt.Errorf("codex judgment policy: %w", err)
	}
	return judgment, meta, nil
}

func (c Codex) Execute(ctx context.Context, request ExecutionRequest) (ExecutionReport, Metadata, error) {
	if err := domain.ValidateEvidenceContract(request.Contract); err != nil {
		return ExecutionReport{}, Metadata{}, err
	}
	prompt, err := executionPrompt(request.Contract, request.Context)
	if err != nil {
		return ExecutionReport{}, Metadata{}, err
	}
	args := c.baseArgs("workspace-write", request.Worktree)
	args = append(args,
		"--config", "sandbox_workspace_write.network_access=false",
		"--config", `approval_policy="never"`,
		"--output-schema="+request.SchemaPath,
		"--output-last-message="+request.OutputPath,
		"--color=never", "--json", "-",
	)
	result, err := c.run(ctx, args, []byte(prompt))
	meta := Metadata{Backend: c.Name(), Model: c.Model, Transcript: result.Stdout, Stderr: result.Stderr}
	if err != nil {
		return ExecutionReport{}, meta, err
	}
	var report ExecutionReport
	if err := decodeFile(request.OutputPath, &report); err != nil {
		return ExecutionReport{}, meta, fmt.Errorf("codex execution: %w", err)
	}
	if err := validateExecutionReport(report, request.Contract); err != nil {
		return ExecutionReport{}, meta, err
	}
	return report, meta, nil
}

func (c Codex) Review(ctx context.Context, request ReviewRequest) (domain.Review, Metadata, error) {
	sanitized, err := SanitizeObservation(request.Material, request.MaxInputBytes)
	if err != nil {
		return domain.Review{}, Metadata{}, err
	}
	prompt, err := reviewPrompt(request, sanitized)
	if err != nil {
		return domain.Review{}, Metadata{}, err
	}
	args := c.baseArgs("read-only", request.WorkingDir)
	args = append(args,
		"--output-schema="+request.SchemaPath,
		"--output-last-message="+request.OutputPath,
		"--color=never", "--json", "-",
	)
	result, err := c.run(ctx, args, []byte(prompt))
	meta := Metadata{Backend: c.Name(), Model: c.Model, Transcript: result.Stdout, Stderr: result.Stderr}
	if err != nil {
		return domain.Review{}, meta, err
	}
	var review domain.Review
	if err := decodeFile(request.OutputPath, &review); err != nil {
		return domain.Review{}, meta, fmt.Errorf("codex review: %w", err)
	}
	if err := validateReview(review, request.ContractHash); err != nil {
		return domain.Review{}, meta, err
	}
	return review, meta, nil
}

func (c Codex) baseArgs(sandbox, dir string) []string {
	args := []string{"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--sandbox=" + sandbox, "--cd=" + dir}
	if c.Model != "" {
		args = append(args, "--model="+c.Model)
	}
	return args
}

func (c Codex) run(ctx context.Context, args []string, stdin []byte) (adapters.Result, error) {
	if c.Runner == nil {
		return adapters.Result{}, fmt.Errorf("codex runner is required")
	}
	binary := c.Binary
	if binary == "" {
		binary = "codex"
	}
	result, err := c.Runner.Run(ctx, adapters.Invocation{
		Name: binary, Args: args, Stdin: stdin, Env: safeEnvironment(), MaxOutputBytes: 8 << 20,
	})
	if err != nil {
		return result, fmt.Errorf("codex backend: %w", err)
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("codex backend exited %d", result.ExitCode)
	}
	return result, nil
}

func decodeFile(path string, target any) error {
	if path == "" {
		return fmt.Errorf("output path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read structured output: %w", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode structured output: %w", err)
	}
	return nil
}

func validateReview(review domain.Review, contractHash string) error {
	if review.SchemaVersion != domain.ReviewSchemaV1 {
		return fmt.Errorf("review schema_version must be %q", domain.ReviewSchemaV1)
	}
	if review.ContractHash != contractHash {
		return fmt.Errorf("review contract_hash does not match")
	}
	if review.Verdict != domain.VerdictPromote && review.Verdict != domain.VerdictCloseSuccess && review.Verdict != domain.VerdictCloseFailure && review.Verdict != domain.VerdictInconclusive {
		return fmt.Errorf("review verdict %q is invalid", review.Verdict)
	}
	if review.Rationale == "" || len(review.Evidence) == 0 {
		return fmt.Errorf("review rationale and evidence are required")
	}
	return nil
}
