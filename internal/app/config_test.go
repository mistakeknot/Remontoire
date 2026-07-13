package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cyclepkg "github.com/mistakeknot/Remontoire/internal/cycle"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

func validConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "projects")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, name := range []string{"judgment.json", "execution.json", "review.json"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths[name] = path
	}
	script := filepath.Join(root, "sync-roadmap-json.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Config{
		Version: 1, Portfolio: "sylveste", ProjectDir: project,
		ArtifactRoot: filepath.Join(root, "artifacts"), WorktreeRoot: filepath.Join(root, "worktrees"),
		AllowedRepositoryRoots: []string{project},
		JudgmentSchemaPath:     paths["judgment.json"], ExecutionSchemaPath: paths["execution.json"], ReviewSchemaPath: paths["review.json"],
		RoadmapScriptPath: script, RoadmapPath: filepath.Join(root, "roadmap.json"),
		DefaultMode: domain.ModeProposal, JudgeBackend: "codex", ReviewerBackend: "claude",
		IntercoreBinary: "ic", BeadsBinary: "bd", OckhamBinary: "ockham", GitBinary: "git", BashBinary: "bash",
		CodexBinary: "codex", ClaudeBinary: "claude",
	}
}

func TestLoadConfigRejectsUnknownFieldsAndRelativeAuthorityPaths(t *testing.T) {
	cfg := validConfig(t)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"unexpected":true}`)...)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}

	cfg.ProjectDir = "relative/projects"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative path error = %v", err)
	}
}

func TestLoadConfigRejectsDuplicateAuthorityFields(t *testing.T) {
	cfg := validConfig(t)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"portfolio":"other"}`)...)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate field error = %v", err)
	}
}

func TestLoadConfigAppliesBoundedDefaults(t *testing.T) {
	cfg := validConfig(t)
	cfg.MaxInputBytes = 0
	cfg.DiscoveryLimit = 0
	cfg.LockTimeout = ""
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MaxInputBytes != 1<<20 || loaded.DiscoveryLimit != 100 || loaded.LockTimeout != "0s" {
		t.Fatalf("defaults = bytes:%d discoveries:%d lock:%q", loaded.MaxInputBytes, loaded.DiscoveryLimit, loaded.LockTimeout)
	}
}

func TestConfigRejectsWhitespacePaddedAuthorityValues(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"portfolio": func(cfg *Config) { cfg.Portfolio = " sylveste" },
		"backend":   func(cfg *Config) { cfg.JudgeBackend = "codex " },
		"binary":    func(cfg *Config) { cfg.CodexBinary = " codex" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.applyDefaults()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestNewWiresExistingAdaptersIntoCycleService(t *testing.T) {
	application, err := New(validConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	service, ok := application.Service.(*cyclepkg.Service)
	if !ok {
		t.Fatalf("service type = %T", application.Service)
	}
	if application.State == nil || service.Kernel == nil || service.Backlog == nil || service.Policy == nil || service.Roadmap == nil || service.Worktrees == nil {
		t.Fatalf("application dependencies are incomplete: %#v", service)
	}
	if len(service.Executors) != 2 || len(service.Reviewers) != 2 || service.Judge == nil || service.Config.RoadmapPath != application.Config.RoadmapPath {
		t.Fatalf("backend/config wiring = %#v", service)
	}
}

func TestExampleConfigParsesStrictly(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "config", "remontoire.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
}
