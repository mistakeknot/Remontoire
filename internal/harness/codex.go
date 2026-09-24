package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

type Codex struct {
	Binary       string
	Model        string
	PoolInputDir string
	Runner       adapters.Runner
}

const (
	CodexTransportPooled          = "pooled"
	CodexTransportPoolUnconfirmed = "pool-unconfirmed"
	CodexTransportDirectFallback  = "direct-fallback"
	poolStartMarker               = "bb-pool-exec: transport=pooled provider=codex"
	poolUnconfirmedMarker         = "bb-pool-exec: transport=pool-unconfirmed provider=codex"
	poolExecTimeout               = 29 * time.Minute
)

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
	result, transport, err := c.run(ctx, args, []byte(judgmentPrompt(sanitized)))
	meta := Metadata{Backend: c.Name(), Model: c.Model, Transport: transport, Turns: codexTurns(result.Stdout), Transcript: result.Stdout, Stderr: result.Stderr}
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
		"--output-schema="+request.SchemaPath,
		"--output-last-message="+request.OutputPath,
		"--color=never", "--json", "-",
	)
	result, transport, err := c.run(ctx, args, []byte(prompt))
	meta := Metadata{Backend: c.Name(), Model: c.Model, Transport: transport, Turns: codexTurns(result.Stdout), Transcript: result.Stdout, Stderr: result.Stderr}
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
	result, transport, err := c.run(ctx, args, []byte(prompt))
	meta := Metadata{Backend: c.Name(), Model: c.Model, Transport: transport, Turns: codexTurns(result.Stdout), Transcript: result.Stdout, Stderr: result.Stderr}
	if err != nil {
		return domain.Review{}, meta, err
	}
	var review domain.Review
	if err := decodeFile(request.OutputPath, &review); err != nil {
		return domain.Review{}, meta, fmt.Errorf("codex review: %w", err)
	}
	if err := ValidateReview(review, request.ContractHash); err != nil {
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

func (c Codex) run(ctx context.Context, args []string, stdin []byte) (adapters.Result, string, error) {
	if c.Runner == nil {
		return adapters.Result{}, "", fmt.Errorf("codex runner is required")
	}
	binary := c.Binary
	if binary == "" {
		binary = "codex"
	}
	environment, cleanup, err := safeEnvironment()
	if err != nil {
		return adapters.Result{}, "", fmt.Errorf("codex environment: %w", err)
	}
	defer cleanup()
	stdinPath, err := writePoolInput(c.PoolInputDir, stdin)
	if err != nil {
		return adapters.Result{}, "", fmt.Errorf("codex pool input: %w", err)
	}
	defer os.Remove(stdinPath)
	poolArgs := []string{"pool", "exec", "--stdin-file", stdinPath, "--", "codex"}
	poolArgs = append(poolArgs, args...)
	poolCtx, cancelPool := context.WithTimeout(ctx, poolExecTimeout)
	result, poolErr := c.Runner.Run(poolCtx, adapters.Invocation{
		Name: "bb", Args: poolArgs, Env: environment, MaxOutputBytes: 8 << 20,
	})
	cancelPool()
	transport := CodexTransportPoolUnconfirmed
	if bytes.HasPrefix(result.Stderr, []byte(poolStartMarker+"\n")) {
		transport = CodexTransportPooled
	}
	if poolErr == nil && result.ExitCode == 0 {
		if transport != CodexTransportPooled {
			return result, transport, fmt.Errorf("codex pool did not confirm its provider pin; command will not be replayed")
		}
		return result, transport, nil
	}
	if poolMayHaveStarted(ctx, result, poolErr) {
		if poolErr != nil {
			return result, transport, fmt.Errorf("codex pooled backend: %w%s", poolErr, codexErrorDetail(result))
		}
		return result, transport, fmt.Errorf("codex pooled backend exited %d%s", result.ExitCode, codexErrorDetail(result))
	}
	directArgs := append([]string{}, args...)
	if len(directArgs) > 0 && directArgs[0] == "exec" {
		directArgs = append([]string{"exec", "--config", "sandbox_workspace_write.network_access=false", "--config", `approval_policy="never"`}, directArgs[1:]...)
	}
	result, err = c.Runner.Run(ctx, adapters.Invocation{
		Name: binary, Args: directArgs, Stdin: stdin, Env: environment, MaxOutputBytes: 8 << 20,
	})
	result.Stderr = append([]byte("remontoire-codex: transport=direct-fallback\n"), result.Stderr...)
	if err != nil {
		return result, CodexTransportDirectFallback, fmt.Errorf("codex backend: %w%s", err, codexErrorDetail(result))
	}
	if result.ExitCode != 0 {
		return result, CodexTransportDirectFallback, fmt.Errorf("codex backend exited %d%s", result.ExitCode, codexErrorDetail(result))
	}
	return result, CodexTransportDirectFallback, nil
}

func writePoolInput(dir string, stdin []byte) (string, error) {
	file, err := os.CreateTemp(dir, ".remontoire-codex-input-")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.Write(stdin); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func poolMayHaveStarted(ctx context.Context, result adapters.Result, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, adapters.ErrOutputLimit) {
		return true
	}
	output := strings.ToLower(string(result.Stdout) + "\n" + string(result.Stderr))
	if strings.Contains(output, poolStartMarker) ||
		strings.Contains(output, poolUnconfirmedMarker) ||
		strings.Contains(output, "plugin_cli_output_too_large") ||
		strings.Contains(output, "plugin cli output") && strings.Contains(output, "large") {
		return true
	}
	var execError *exec.Error
	if errors.As(err, &execError) || errors.Is(err, exec.ErrNotFound) {
		return false
	}
	for _, knownPreStart := range []string{
		"account pooler cannot currently serve codex",
		"account pooler could not reach its command runner",
		"no primary enrolled host",
		"unknown command 'pool'",
		"econnrefused",
		"connection refused",
	} {
		if strings.Contains(output, knownPreStart) {
			return false
		}
	}
	return true
}

func codexErrorDetail(result adapters.Result) string {
	scanner := bufio.NewScanner(bytes.NewReader(result.Stdout))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	message := ""
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "error" && event.Message != "" {
			message = event.Message
		}
	}
	for _, pattern := range sensitiveValue {
		message = pattern.ReplaceAllString(message, "[REDACTED]")
	}
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		message = message[:500]
	}
	if message == "" {
		return ""
	}
	return "; codex JSON error: " + message
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

func LoadExecutionReport(path string, contract domain.EvidenceContract) (ExecutionReport, error) {
	var report ExecutionReport
	if err := decodeFile(path, &report); err != nil {
		return ExecutionReport{}, err
	}
	if err := validateExecutionReport(report, contract); err != nil {
		return ExecutionReport{}, err
	}
	return report, nil
}

func LoadReview(path, contractHash string) (domain.Review, error) {
	var review domain.Review
	if err := decodeFile(path, &review); err != nil {
		return domain.Review{}, err
	}
	if err := ValidateReview(review, contractHash); err != nil {
		return domain.Review{}, err
	}
	return review, nil
}

func ValidateReview(review domain.Review, contractHash string) error {
	if review.SchemaVersion != domain.ReviewSchemaV1 {
		return fmt.Errorf("review schema_version must be %q", domain.ReviewSchemaV1)
	}
	if review.ContractHash != contractHash {
		return fmt.Errorf("review contract_hash does not match")
	}
	if review.Verdict != domain.VerdictPromote && review.Verdict != domain.VerdictCloseSuccess && review.Verdict != domain.VerdictCloseFailure && review.Verdict != domain.VerdictInconclusive {
		return fmt.Errorf("review verdict %q is invalid", review.Verdict)
	}
	if strings.TrimSpace(review.Rationale) == "" || len(review.Evidence) == 0 {
		return fmt.Errorf("review rationale and evidence are required")
	}
	return nil
}

func codexTurns(transcript []byte) int {
	turns := 0
	scanner := bufio.NewScanner(bytes.NewReader(transcript))
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "turn.started" {
			turns++
		}
	}
	return turns
}
