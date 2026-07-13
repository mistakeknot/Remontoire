package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

const ExecutionSchemaV1 = "remontoire.execution/v1"

type JudgmentRequest struct {
	WorkingDir    string
	SchemaPath    string
	SchemaJSON    []byte
	OutputPath    string
	Observation   []byte
	MaxInputBytes int
	MaxBudgetUSD  float64
}

type ExecutionRequest struct {
	Worktree   string
	SchemaPath string
	SchemaJSON []byte
	OutputPath string
	Contract   domain.EvidenceContract
	Context    []byte
}

type ReviewRequest struct {
	WorkingDir    string
	SchemaPath    string
	SchemaJSON    []byte
	OutputPath    string
	Contract      domain.EvidenceContract
	ContractHash  string
	Material      []byte
	MaxInputBytes int
	MaxBudgetUSD  float64
}

type ExecutionReport struct {
	SchemaVersion string   `json:"schema_version"`
	Summary       string   `json:"summary"`
	ChangedPaths  []string `json:"changed_paths"`
	Commands      []string `json:"commands"`
	Completed     bool     `json:"completed"`
}

type Metadata struct {
	Backend    string  `json:"backend"`
	Model      string  `json:"model"`
	Turns      int     `json:"turns,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	Transcript []byte  `json:"-"`
	Stderr     []byte  `json:"-"`
}

type Backend interface {
	Name() string
	Judge(context.Context, JudgmentRequest) (domain.Judgment, Metadata, error)
	Execute(context.Context, ExecutionRequest) (ExecutionReport, Metadata, error)
	Review(context.Context, ReviewRequest) (domain.Review, Metadata, error)
}

func validateExecutionReport(report ExecutionReport, contract domain.EvidenceContract) error {
	if report.SchemaVersion != ExecutionSchemaV1 {
		return fmt.Errorf("execution schema_version must be %q", ExecutionSchemaV1)
	}
	if strings.TrimSpace(report.Summary) == "" {
		return fmt.Errorf("execution summary is required")
	}
	for _, changed := range report.ChangedPaths {
		if filepath.IsAbs(changed) {
			return fmt.Errorf("execution changed path %q is absolute", changed)
		}
		cleaned := filepath.ToSlash(filepath.Clean(changed))
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return fmt.Errorf("execution changed path %q is unsafe", changed)
		}
		covered := false
		for _, allowed := range contract.AllowedPaths {
			allowed = strings.TrimSuffix(filepath.ToSlash(filepath.Clean(allowed)), "/")
			if cleaned == allowed || strings.HasPrefix(cleaned, allowed+"/") {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("execution changed path %q is outside allowed paths", changed)
		}
	}
	return nil
}
