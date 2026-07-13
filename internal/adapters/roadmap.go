package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

type Roadmap struct {
	BashBinary string
	ScriptPath string
	Dir        string
	OutputPath string
	Runner     Runner
}

func (r Roadmap) Sync(ctx context.Context) (string, error) {
	if r.Runner == nil {
		return "", fmt.Errorf("roadmap runner is required")
	}
	if r.ScriptPath == "" || r.OutputPath == "" {
		return "", fmt.Errorf("roadmap script and output paths are required")
	}
	binary := r.BashBinary
	if binary == "" {
		binary = "bash"
	}
	result, err := r.Runner.Run(ctx, Invocation{
		Name: binary,
		Args: []string{r.ScriptPath, r.OutputPath},
		Dir:  r.Dir,
	})
	if err != nil {
		return "", fmt.Errorf("sync roadmap: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("sync roadmap exited %d", result.ExitCode)
	}
	content, err := os.ReadFile(r.OutputPath)
	if err != nil {
		return "", fmt.Errorf("read generated roadmap: %w", err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}
