package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerPassesArgumentsWithoutShellExpansion(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "expanded")
	payload := "$(touch " + marker + ")"
	runner := ExecRunner{DefaultMaxOutputBytes: 1024}

	result, err := runner.Run(context.Background(), Invocation{
		Name: "/usr/bin/printf",
		Args: []string{"%s", payload},
		Dir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != payload {
		t.Fatalf("stdout = %q, want %q", result.Stdout, payload)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell expansion occurred; marker stat error = %v", err)
	}
}

func TestExecRunnerBoundsOutput(t *testing.T) {
	runner := ExecRunner{DefaultMaxOutputBytes: 8}
	_, err := runner.Run(context.Background(), Invocation{
		Name: "/usr/bin/printf",
		Args: []string{"%s", strings.Repeat("x", 64)},
	})
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("error = %v, want ErrOutputLimit", err)
	}
}

func TestExecRunnerHonorsContextCancellation(t *testing.T) {
	runner := ExecRunner{DefaultMaxOutputBytes: 1024}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := runner.Run(ctx, Invocation{Name: "/bin/sleep", Args: []string{"1"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
}
