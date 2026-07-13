package adapters

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type WorktreeInfo struct {
	Path       string `json:"path"`
	BaseCommit string `json:"base_commit"`
}

type GitWorktrees struct {
	GitBinary string
	Root      string
	Runner    Runner
}

func (g GitWorktrees) Prepare(ctx context.Context, repository, cycleID string) (WorktreeInfo, error) {
	if !filepath.IsAbs(repository) || !filepath.IsAbs(g.Root) {
		return WorktreeInfo{}, fmt.Errorf("repository and worktree root must be absolute")
	}
	if cycleID == "" || cycleID != filepath.Base(cycleID) || strings.ContainsAny(cycleID, "/\\\x00") {
		return WorktreeInfo{}, fmt.Errorf("unsafe cycle id %q", cycleID)
	}
	baseResult, err := g.run(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		return WorktreeInfo{}, fmt.Errorf("resolve worktree base: %w", err)
	}
	base := strings.TrimSpace(string(baseResult.Stdout))
	if len(base) < 40 {
		return WorktreeInfo{}, fmt.Errorf("git returned invalid base commit %q", base)
	}
	destination := filepath.Join(g.Root, cycleID)
	if _, err := os.Stat(destination); err == nil {
		result, verifyErr := g.run(ctx, destination, "rev-parse", "HEAD")
		if verifyErr != nil {
			return WorktreeInfo{}, fmt.Errorf("existing worktree is invalid: %w", verifyErr)
		}
		return WorktreeInfo{Path: destination, BaseCommit: strings.TrimSpace(string(result.Stdout))}, nil
	} else if !os.IsNotExist(err) {
		return WorktreeInfo{}, err
	}
	if err := os.MkdirAll(g.Root, 0o700); err != nil {
		return WorktreeInfo{}, fmt.Errorf("create worktree root: %w", err)
	}
	if _, err := g.run(ctx, repository, "worktree", "add", "--detach", destination, base); err != nil {
		return WorktreeInfo{}, fmt.Errorf("create detached worktree: %w", err)
	}
	return WorktreeInfo{Path: destination, BaseCommit: base}, nil
}

func (g GitWorktrees) ChangedPaths(ctx context.Context, worktree string) ([]string, error) {
	tracked, err := g.run(ctx, worktree, "diff", "--name-only", "-z", "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("list tracked changes: %w", err)
	}
	untracked, err := g.run(ctx, worktree, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, fmt.Errorf("list untracked changes: %w", err)
	}
	set := map[string]bool{}
	for _, value := range append(parseNUL(tracked.Stdout), parseNUL(untracked.Stdout)...) {
		cleaned := path.Clean(filepath.ToSlash(value))
		if cleaned != "." {
			set[cleaned] = true
		}
	}
	paths := make([]string, 0, len(set))
	for value := range set {
		paths = append(paths, value)
	}
	sort.Strings(paths)
	return paths, nil
}

func (g GitWorktrees) Patch(ctx context.Context, worktree string, _ []string) ([]byte, error) {
	untrackedResult, err := g.run(ctx, worktree, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, err
	}
	untracked := parseNUL(untrackedResult.Stdout)
	if len(untracked) > 0 {
		args := append([]string{"add", "-N", "--"}, untracked...)
		if _, err := g.run(ctx, worktree, args...); err != nil {
			return nil, fmt.Errorf("mark untracked paths for patch: %w", err)
		}
	}
	result, err := g.run(ctx, worktree, "diff", "--binary", "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("create experiment patch: %w", err)
	}
	return result.Stdout, nil
}

func CheckAllowedPaths(changed, allowed []string) error {
	if len(changed) == 0 {
		return nil
	}
	if len(allowed) == 0 {
		return fmt.Errorf("no allowed paths configured")
	}
	for _, changedPath := range changed {
		if path.IsAbs(changedPath) || filepath.IsAbs(changedPath) {
			return fmt.Errorf("changed path %q is absolute", changedPath)
		}
		cleaned := path.Clean(filepath.ToSlash(changedPath))
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return fmt.Errorf("changed path %q is unsafe", changedPath)
		}
		covered := false
		for _, prefix := range allowed {
			prefix = strings.TrimSuffix(path.Clean(filepath.ToSlash(prefix)), "/")
			if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("changed path %q is outside allowed paths", changedPath)
		}
	}
	return nil
}

func (g GitWorktrees) run(ctx context.Context, dir string, args ...string) (Result, error) {
	if g.Runner == nil {
		return Result{}, fmt.Errorf("git worktree runner is required")
	}
	binary := g.GitBinary
	if binary == "" {
		binary = "git"
	}
	result, err := g.Runner.Run(ctx, Invocation{Name: binary, Args: args, Dir: dir, MaxOutputBytes: 16 << 20})
	if err != nil {
		return result, err
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("git %s exited %d", strings.Join(args[:min(2, len(args))], " "), result.ExitCode)
	}
	return result, nil
}

func parseNUL(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
