package app

import (
	"os/exec"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/cycle"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

func New(cfg Config) (*Application, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	runner := adapters.ExecRunner{DefaultMaxOutputBytes: 8 << 20}
	intercore := &adapters.Intercore{Binary: cfg.IntercoreBinary, Dir: cfg.ProjectDir, Runner: runner}
	beads := &adapters.Beads{Binary: cfg.BeadsBinary, Dir: cfg.ProjectDir, Runner: runner}
	ockham := &adapters.Ockham{Binary: cfg.OckhamBinary, Dir: cfg.ProjectDir, Runner: runner}
	roadmap := &adapters.Roadmap{
		BashBinary: cfg.BashBinary, ScriptPath: cfg.RoadmapScriptPath, Dir: cfg.ProjectDir,
		OutputPath: cfg.RoadmapPath, Runner: runner,
	}
	worktrees := &adapters.GitWorktrees{GitBinary: cfg.GitBinary, Root: cfg.WorktreeRoot, Runner: runner}
	codex := &harness.Codex{Binary: cfg.CodexBinary, Model: cfg.CodexModel, Runner: runner}
	claude := &harness.Claude{Binary: cfg.ClaudeBinary, Model: cfg.ClaudeModel, Runner: runner}
	executors := map[string]cycle.Executor{"codex": codex, "claude": claude}
	reviewers := map[string]cycle.Reviewer{"codex": codex, "claude": claude}
	var judge cycle.Judge
	if strings.EqualFold(cfg.JudgeBackend, "claude") {
		judge = claude
	} else {
		judge = codex
	}
	store := cycle.FileStore{Root: cfg.ArtifactRoot}
	service := &cycle.Service{
		Config: cycle.Config{
			Portfolio: cfg.Portfolio, ProjectDir: cfg.ProjectDir, ArtifactRoot: cfg.ArtifactRoot,
			JudgmentSchemaPath: cfg.JudgmentSchemaPath, ExecutionSchemaPath: cfg.ExecutionSchemaPath,
			ReviewSchemaPath: cfg.ReviewSchemaPath, ReviewerBackend: strings.ToLower(cfg.ReviewerBackend),
			RoadmapPath: cfg.RoadmapPath, WorktreeRoot: cfg.WorktreeRoot,
			AllowedRepositoryRoots: append([]string(nil), cfg.AllowedRepositoryRoots...),
			MaxInputBytes:          cfg.MaxInputBytes, DiscoveryLimit: cfg.DiscoveryLimit,
			LockTimeout: cfg.LockTimeout, DefaultMode: cfg.DefaultMode,
		},
		Kernel: intercore, Backlog: beads, Policy: ockham, Judge: judge,
		Executors: executors, Reviewers: reviewers, Roadmap: roadmap, Worktrees: worktrees,
		BenchmarkRunner: runner, Store: store,
	}
	return &Application{Config: cfg, Service: service, State: intercore, Store: store, LookPath: exec.LookPath}, nil
}
