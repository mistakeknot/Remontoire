package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/strictjson"
)

type Config struct {
	Version                int         `json:"version"`
	Portfolio              string      `json:"portfolio"`
	ProjectDir             string      `json:"project_dir"`
	ArtifactRoot           string      `json:"artifact_root"`
	WorktreeRoot           string      `json:"worktree_root"`
	AllowedRepositoryRoots []string    `json:"allowed_repository_roots"`
	JudgmentSchemaPath     string      `json:"judgment_schema_path"`
	ExecutionSchemaPath    string      `json:"execution_schema_path"`
	ReviewSchemaPath       string      `json:"review_schema_path"`
	RoadmapScriptPath      string      `json:"roadmap_script_path"`
	RoadmapPath            string      `json:"roadmap_path"`
	DefaultMode            domain.Mode `json:"default_mode"`
	JudgeBackend           string      `json:"judge_backend"`
	ReviewerBackend        string      `json:"reviewer_backend"`
	MaxInputBytes          int         `json:"max_input_bytes,omitempty"`
	DiscoveryLimit         int         `json:"discovery_limit,omitempty"`
	LockTimeout            string      `json:"lock_timeout,omitempty"`
	IntercoreBinary        string      `json:"intercore_binary,omitempty"`
	BeadsBinary            string      `json:"beads_binary,omitempty"`
	OckhamBinary           string      `json:"ockham_binary,omitempty"`
	GitBinary              string      `json:"git_binary,omitempty"`
	BashBinary             string      `json:"bash_binary,omitempty"`
	CodexBinary            string      `json:"codex_binary,omitempty"`
	CodexModel             string      `json:"codex_model,omitempty"`
	ClaudeBinary           string      `json:"claude_binary,omitempty"`
	ClaudeModel            string      `json:"claude_model,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if len(data) > 1<<20 {
		return Config{}, fmt.Errorf("config exceeds 1048576 bytes")
	}
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("decode config: multiple JSON values")
		}
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.MaxInputBytes == 0 {
		c.MaxInputBytes = 1 << 20
	}
	if c.DiscoveryLimit == 0 {
		c.DiscoveryLimit = 100
	}
	if c.LockTimeout == "" {
		c.LockTimeout = "0s"
	}
	defaults := map[*string]string{
		&c.IntercoreBinary: "ic", &c.BeadsBinary: "bd", &c.OckhamBinary: "ockham",
		&c.GitBinary: "git", &c.BashBinary: "bash", &c.CodexBinary: "codex", &c.ClaudeBinary: "claude",
	}
	for target, value := range defaults {
		if *target == "" {
			*target = value
		}
	}
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("config version must be 1")
	}
	if strings.TrimSpace(c.Portfolio) == "" || strings.TrimSpace(c.Portfolio) != c.Portfolio {
		return fmt.Errorf("portfolio is invalid")
	}
	paths := map[string]string{
		"project_dir": c.ProjectDir, "artifact_root": c.ArtifactRoot, "worktree_root": c.WorktreeRoot,
		"judgment_schema_path": c.JudgmentSchemaPath, "execution_schema_path": c.ExecutionSchemaPath,
		"review_schema_path": c.ReviewSchemaPath, "roadmap_script_path": c.RoadmapScriptPath, "roadmap_path": c.RoadmapPath,
	}
	for name, value := range paths {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return fmt.Errorf("%s must be a clean absolute path", name)
		}
	}
	if len(c.AllowedRepositoryRoots) == 0 {
		return fmt.Errorf("allowed_repository_roots must not be empty")
	}
	for _, root := range c.AllowedRepositoryRoots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return fmt.Errorf("allowed_repository_roots must contain clean absolute paths")
		}
	}
	if c.DefaultMode != domain.ModeProposal && c.DefaultMode != domain.ModeShadow {
		return fmt.Errorf("default_mode must be proposal or shadow")
	}
	if strings.TrimSpace(c.JudgeBackend) != c.JudgeBackend || strings.TrimSpace(c.ReviewerBackend) != c.ReviewerBackend || !supportedBackend(c.JudgeBackend) || !supportedBackend(c.ReviewerBackend) {
		return fmt.Errorf("judge_backend and reviewer_backend must be codex or claude")
	}
	if strings.EqualFold(c.JudgeBackend, c.ReviewerBackend) {
		return fmt.Errorf("judge_backend and reviewer_backend must be different")
	}
	if c.MaxInputBytes < 1024 || c.MaxInputBytes > 8<<20 {
		return fmt.Errorf("max_input_bytes must be between 1024 and %d", 8<<20)
	}
	if c.DiscoveryLimit < 1 || c.DiscoveryLimit > 1000 {
		return fmt.Errorf("discovery_limit must be between 1 and 1000")
	}
	duration, err := time.ParseDuration(c.LockTimeout)
	if err != nil || duration < 0 {
		return fmt.Errorf("lock_timeout must be a non-negative duration")
	}
	for name, value := range map[string]string{
		"intercore_binary": c.IntercoreBinary, "beads_binary": c.BeadsBinary, "ockham_binary": c.OckhamBinary,
		"git_binary": c.GitBinary, "bash_binary": c.BashBinary, "codex_binary": c.CodexBinary, "claude_binary": c.ClaudeBinary,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	return nil
}

func supportedBackend(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "codex", "claude":
		return true
	default:
		return false
	}
}
