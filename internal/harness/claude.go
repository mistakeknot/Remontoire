package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

type Claude struct {
	Binary string
	Model  string
	Runner adapters.Runner
}

func (c Claude) Name() string { return "claude" }

func (c Claude) Judge(ctx context.Context, request JudgmentRequest) (domain.Judgment, Metadata, error) {
	sanitized, err := SanitizeObservation(request.Observation, request.MaxInputBytes)
	if err != nil {
		return domain.Judgment{}, Metadata{}, err
	}
	schema, err := schemaJSON(request.SchemaJSON, request.SchemaPath)
	if err != nil {
		return domain.Judgment{}, Metadata{}, err
	}
	args := c.readOnlyArgs(schema, request.MaxBudgetUSD)
	result, err := c.run(ctx, request.WorkingDir, args, []byte(judgmentPrompt(sanitized)))
	meta := Metadata{Backend: c.Name(), Model: c.Model, Transcript: result.Stdout, Stderr: result.Stderr}
	if err != nil {
		return domain.Judgment{}, meta, err
	}
	var judgment domain.Judgment
	turns, cost, decodeErr := decodeClaudeWithUsage(result.Stdout, &judgment)
	meta.Turns, meta.CostUSD = turns, cost
	if decodeErr != nil {
		return domain.Judgment{}, meta, fmt.Errorf("claude judgment: %w", decodeErr)
	}
	if err := domain.ValidateJudgment(judgment); err != nil {
		return domain.Judgment{}, meta, fmt.Errorf("claude judgment policy: %w", err)
	}
	return judgment, meta, nil
}

func (c Claude) Execute(ctx context.Context, request ExecutionRequest) (ExecutionReport, Metadata, error) {
	if err := domain.ValidateEvidenceContract(request.Contract); err != nil {
		return ExecutionReport{}, Metadata{}, err
	}
	schema, err := schemaJSON(request.SchemaJSON, request.SchemaPath)
	if err != nil {
		return ExecutionReport{}, Metadata{}, err
	}
	prompt, err := executionPrompt(request.Contract, request.Context)
	if err != nil {
		return ExecutionReport{}, Metadata{}, err
	}
	program := filepath.Base(request.Contract.Benchmark[0])
	allowed := "Read,Glob,Grep,Edit,Write,Bash(" + program + " *)"
	args := []string{
		"-p", "--no-session-persistence", "--disable-slash-commands", "--no-chrome",
		"--permission-mode=dontAsk",
		"--tools=Read,Glob,Grep,Edit,Write,Bash",
		"--allowedTools=" + allowed,
		"--disallowedTools=WebFetch,WebSearch,NotebookEdit",
	}
	args = appendModelBudgetSchema(args, c.Model, request.Contract.Budget.MaxCostUSD, schema)
	result, err := c.run(ctx, request.Worktree, args, []byte(prompt))
	meta := Metadata{Backend: c.Name(), Model: c.Model, Transcript: result.Stdout, Stderr: result.Stderr}
	if err != nil {
		return ExecutionReport{}, meta, err
	}
	var report ExecutionReport
	turns, cost, decodeErr := decodeClaudeWithUsage(result.Stdout, &report)
	meta.Turns, meta.CostUSD = turns, cost
	if decodeErr != nil {
		return ExecutionReport{}, meta, fmt.Errorf("claude execution: %w", decodeErr)
	}
	if err := validateExecutionReport(report, request.Contract); err != nil {
		return ExecutionReport{}, meta, err
	}
	return report, meta, nil
}

func (c Claude) Review(ctx context.Context, request ReviewRequest) (domain.Review, Metadata, error) {
	sanitized, err := SanitizeObservation(request.Material, request.MaxInputBytes)
	if err != nil {
		return domain.Review{}, Metadata{}, err
	}
	schema, err := schemaJSON(request.SchemaJSON, request.SchemaPath)
	if err != nil {
		return domain.Review{}, Metadata{}, err
	}
	prompt, err := reviewPrompt(request, sanitized)
	if err != nil {
		return domain.Review{}, Metadata{}, err
	}
	args := c.readOnlyArgs(schema, request.MaxBudgetUSD)
	result, err := c.run(ctx, request.WorkingDir, args, []byte(prompt))
	meta := Metadata{Backend: c.Name(), Model: c.Model, Transcript: result.Stdout, Stderr: result.Stderr}
	if err != nil {
		return domain.Review{}, meta, err
	}
	var review domain.Review
	turns, cost, decodeErr := decodeClaudeWithUsage(result.Stdout, &review)
	meta.Turns, meta.CostUSD = turns, cost
	if decodeErr != nil {
		return domain.Review{}, meta, decodeErr
	}
	if err := ValidateReview(review, request.ContractHash); err != nil {
		return domain.Review{}, meta, err
	}
	return review, meta, nil
}

func (c Claude) readOnlyArgs(schema string, maxBudget float64) []string {
	args := []string{
		"-p", "--no-session-persistence", "--disable-slash-commands", "--no-chrome",
		"--permission-mode=dontAsk",
		"--tools=Read,Glob,Grep",
		"--allowedTools=Read,Glob,Grep",
		"--disallowedTools=Bash,Edit,Write,WebFetch,WebSearch,NotebookEdit",
	}
	return appendModelBudgetSchema(args, c.Model, maxBudget, schema)
}

func appendModelBudgetSchema(args []string, model string, budget float64, schema string) []string {
	if model != "" {
		args = append(args, "--model="+model)
	}
	if budget <= 0 {
		budget = 1
	}
	args = append(args,
		"--max-budget-usd="+strconv.FormatFloat(budget, 'f', -1, 64),
		"--json-schema="+schema,
		"--output-format=json",
	)
	return args
}

func (c Claude) run(ctx context.Context, dir string, args []string, stdin []byte) (adapters.Result, error) {
	if c.Runner == nil {
		return adapters.Result{}, fmt.Errorf("claude runner is required")
	}
	binary := c.Binary
	if binary == "" {
		binary = "claude"
	}
	environment, cleanup, err := safeEnvironment()
	if err != nil {
		return adapters.Result{}, fmt.Errorf("claude environment: %w", err)
	}
	defer cleanup()
	result, err := c.Runner.Run(ctx, adapters.Invocation{
		Name: binary, Args: args, Dir: dir, Stdin: stdin, Env: environment, MaxOutputBytes: 8 << 20,
	})
	if err != nil {
		return result, fmt.Errorf("claude backend: %w", err)
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("claude backend exited %d", result.ExitCode)
	}
	return result, nil
}

func schemaJSON(inline []byte, path string) (string, error) {
	data := inline
	if len(data) == 0 {
		if path == "" {
			return "", fmt.Errorf("schema JSON or path is required")
		}
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read schema: %w", err)
		}
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return "", fmt.Errorf("schema is invalid JSON: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func decodeClaude(stdout []byte, target any) error {
	_, _, err := decodeClaudeWithUsage(stdout, target)
	return err
}

func decodeClaudeWithUsage(stdout []byte, target any) (int, float64, error) {
	var envelope struct {
		StructuredOutput json.RawMessage `json:"structured_output"`
		Result           string          `json:"result"`
		IsError          bool            `json:"is_error"`
		Subtype          string          `json:"subtype"`
		NumTurns         int             `json:"num_turns"`
		TotalCostUSD     float64         `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return 0, 0, fmt.Errorf("decode result envelope: %w", err)
	}
	if envelope.IsError || strings.Contains(envelope.Subtype, "error") {
		return envelope.NumTurns, envelope.TotalCostUSD, fmt.Errorf("claude returned an error result")
	}
	payload := envelope.StructuredOutput
	if len(payload) == 0 && envelope.Result != "" {
		payload = json.RawMessage(envelope.Result)
	}
	if len(payload) == 0 {
		return envelope.NumTurns, envelope.TotalCostUSD, fmt.Errorf("claude result has no structured_output")
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return envelope.NumTurns, envelope.TotalCostUSD, fmt.Errorf("decode structured_output: %w", err)
	}
	return envelope.NumTurns, envelope.TotalCostUSD, nil
}
