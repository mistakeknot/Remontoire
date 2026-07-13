package adapters

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitWorktreesPrepareInspectAndPatch(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "allowed.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
		{"add", "internal/allowed.txt", "README.md"},
		{"commit", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}

	manager := GitWorktrees{GitBinary: "git", Root: filepath.Join(filepath.Dir(repo), "worktrees"), Runner: ExecRunner{}}
	info, err := manager.Prepare(context.Background(), repo, "cycle-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.BaseCommit) < 40 || !strings.HasSuffix(info.Path, "cycle-1") {
		t.Fatalf("worktree info = %#v", info)
	}
	second, err := manager.Prepare(context.Background(), repo, "cycle-1")
	if err != nil || second != info {
		t.Fatalf("idempotent prepare = %#v, %v", second, err)
	}

	if err := os.WriteFile(filepath.Join(info.Path, "internal", "allowed.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "internal", "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "README.md"), []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := manager.ChangedPaths(context.Background(), info.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "internal/allowed.txt", "internal/new.txt"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("changed paths = %#v, want %#v", paths, want)
	}
	if err := CheckAllowedPaths(paths, []string{"internal"}); err == nil || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("boundary error = %v", err)
	}
	patch, err := manager.Patch(context.Background(), info.Path, paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"internal/allowed.txt", "internal/new.txt", "README.md"} {
		if !strings.Contains(string(patch), name) {
			t.Fatalf("patch does not contain %s:\n%s", name, patch)
		}
	}
}

func TestCheckAllowedPathsRejectsTraversal(t *testing.T) {
	if err := CheckAllowedPaths([]string{"../other/repo.go"}, []string{"internal"}); err == nil {
		t.Fatal("traversal path was accepted")
	}
}
