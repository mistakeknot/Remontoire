package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/mistakeknot/Remontoire/internal/app"
)

type commandRunner interface {
	Run(context.Context, []string, io.Writer, io.Writer) int
}

type applicationLoader func(string) (commandRunner, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go restoreSignalHandling(ctx, stop)
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, loadApplication))
}

func restoreSignalHandling(ctx context.Context, stop context.CancelFunc) {
	<-ctx.Done()
	stop()
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, load applicationLoader) int {
	jsonMode := hasArgument(args, "--json")
	configPath, remaining, err := takeConfigPath(args)
	if err != nil {
		return configFailure(stderr, jsonMode, err)
	}
	if configPath == "" {
		configPath = os.Getenv("REMONTOIRE_CONFIG")
	}
	if configPath == "" {
		configDir, dirErr := os.UserConfigDir()
		if dirErr != nil {
			return configFailure(stderr, jsonMode, fmt.Errorf("resolve config directory: %w", dirErr))
		}
		configPath = filepath.Join(configDir, "remontoire", "config.json")
	}
	application, err := load(configPath)
	if err != nil {
		return configFailure(stderr, jsonMode, err)
	}
	return application.Run(ctx, remaining, stdout, stderr)
}

func loadApplication(path string) (commandRunner, error) {
	cfg, err := app.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return app.New(cfg)
}

func takeConfigPath(args []string) (string, []string, error) {
	path := ""
	seen := false
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--config=") {
			if seen {
				return "", nil, fmt.Errorf("--config specified more than once")
			}
			seen = true
			path = strings.TrimPrefix(arg, "--config=")
			if path == "" {
				return "", nil, fmt.Errorf("--config requires a path")
			}
			continue
		}
		if arg == "--config" {
			if seen {
				return "", nil, fmt.Errorf("--config specified more than once")
			}
			seen = true
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return "", nil, fmt.Errorf("--config requires one path")
			}
			i++
			path = args[i]
			continue
		}
		remaining = append(remaining, arg)
	}
	return path, remaining, nil
}

func hasArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func configFailure(stderr io.Writer, jsonMode bool, err error) int {
	if jsonMode {
		_ = json.NewEncoder(stderr).Encode(map[string]any{"error": err.Error(), "exit_code": app.ExitUsage})
	} else {
		_, _ = fmt.Fprintln(stderr, "remontoire:", err)
	}
	return app.ExitUsage
}
