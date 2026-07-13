package cycle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

const (
	ObservationSchemaV1 = "remontoire.observation/v1"
	cleanupTimeout      = 15 * time.Second
)

func boundedCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

type Kernel interface {
	Health(context.Context) error
	AcquireCycleLock(context.Context, string, string, string) error
	ReleaseCycleLock(context.Context, string, string) error
	SetCycle(context.Context, domain.Cycle) error
	GetCycle(context.Context, string) (domain.Cycle, error)
	SetLatestCycle(context.Context, string, string) error
	CreateCycleRun(context.Context, string, string, map[string]any) (string, error)
	AdvanceRun(context.Context, string) error
	RunPhase(context.Context, string) (string, error)
	RunStatus(context.Context, string) (string, string, error)
	RecordReplayInput(context.Context, string, string, string, string, string) error
	RecordStageEvent(context.Context, string, string, domain.Stage, string) error
	Observation(context.Context, int) ([]adapters.Discovery, adapters.InterestProfile, error)
	ListOutcomes(context.Context, int) ([]domain.OutcomeSummary, error)
	SetOutcome(context.Context, domain.OutcomeSummary) error
	RecordDiscoveryFeedback(context.Context, string, string, string, string) error
	EmitReceipt(context.Context, string, string, string) (string, error)
	FindReceipt(context.Context, string, string) (string, error)
	VerifyReceipt(context.Context, string) error
}

type Backlog interface {
	List(context.Context) ([]adapters.Bead, error)
	CreateExperiment(context.Context, string, string, domain.Candidate) (string, error)
	CreatePromotion(context.Context, string, string, domain.Candidate, int, string) (string, error)
	AddNote(context.Context, string, string) error
	Close(context.Context, string, string) error
}

type Policy interface {
	Weights(context.Context) (map[string]int, bool)
}

type Judge interface {
	Judge(context.Context, harness.JudgmentRequest) (domain.Judgment, harness.Metadata, error)
}

type Executor interface {
	Execute(context.Context, harness.ExecutionRequest) (harness.ExecutionReport, harness.Metadata, error)
}

type Reviewer interface {
	Review(context.Context, harness.ReviewRequest) (domain.Review, harness.Metadata, error)
}

type Roadmap interface {
	// Sync must be safe to repeat after an interrupted call and return the
	// SHA-256 digest of the generated roadmap artifact.
	Sync(context.Context) (string, error)
}

type WorktreeManager interface {
	Prepare(context.Context, string, string) (adapters.WorktreeInfo, error)
	ChangedPaths(context.Context, string) ([]string, error)
	Patch(context.Context, string, []string) ([]byte, error)
}

type Config struct {
	Portfolio              string
	ProjectDir             string
	ArtifactRoot           string
	JudgmentSchemaPath     string
	ExecutionSchemaPath    string
	ReviewSchemaPath       string
	ReviewerBackend        string
	RoadmapPath            string
	WorktreeRoot           string
	AllowedRepositoryRoots []string
	MaxInputBytes          int
	DiscoveryLimit         int
	LockTimeout            string
	DefaultMode            domain.Mode
}

type Service struct {
	Config          Config
	Kernel          Kernel
	Backlog         Backlog
	Policy          Policy
	Judge           Judge
	Executors       map[string]Executor
	Reviewers       map[string]Reviewer
	Roadmap         Roadmap
	Worktrees       WorktreeManager
	BenchmarkRunner adapters.Runner
	Store           FileStore
	Now             func() time.Time
	NewID           func(time.Time) (string, error)
}

type Observation struct {
	SchemaVersion   string                   `json:"schema_version"`
	CycleID         string                   `json:"cycle_id"`
	Portfolio       string                   `json:"portfolio"`
	CapturedAt      time.Time                `json:"captured_at"`
	Beads           []adapters.Bead          `json:"beads"`
	Discoveries     []adapters.Discovery     `json:"discoveries"`
	InterestProfile adapters.InterestProfile `json:"interest_profile"`
	OckhamWeights   map[string]int           `json:"ockham_weights"`
	OckhamDegraded  bool                     `json:"ockham_degraded"`
	RoadmapDigest   string                   `json:"roadmap_digest,omitempty"`
	PriorOutcomes   []domain.OutcomeSummary  `json:"prior_outcomes"`
	Artifacts       []domain.Artifact        `json:"artifacts"`
}

func (s *Service) Start(ctx context.Context, mode domain.Mode) (cycle domain.Cycle, err error) {
	if err := s.validate(); err != nil {
		return domain.Cycle{}, err
	}
	if mode == "" {
		mode = s.Config.DefaultMode
	}
	if mode != domain.ModeShadow && mode != domain.ModeProposal {
		return domain.Cycle{}, fmt.Errorf("unsupported cycle mode %q", mode)
	}
	now := s.now()
	cycleID, err := s.newID(now)
	if err != nil {
		return domain.Cycle{}, fmt.Errorf("create cycle id: %w", err)
	}
	owner := "remontoire:" + cycleID
	if err := s.Kernel.AcquireCycleLock(ctx, s.Config.Portfolio, owner, s.Config.LockTimeout); err != nil {
		return domain.Cycle{}, err
	}
	defer func() {
		cleanupCtx, cancel := boundedCleanupContext(ctx)
		defer cancel()
		releaseErr := s.Kernel.ReleaseCycleLock(cleanupCtx, s.Config.Portfolio, owner)
		if err == nil && releaseErr != nil {
			err = fmt.Errorf("release cycle lock: %w", releaseErr)
		}
	}()

	cycle = domain.Cycle{
		SchemaVersion:   domain.CycleSchemaV1,
		ID:              cycleID,
		Portfolio:       s.Config.Portfolio,
		Mode:            mode,
		Stage:           domain.StageNew,
		CreatedAt:       now,
		UpdatedAt:       now,
		IdempotencyKeys: map[string]string{},
	}
	if err := s.persist(ctx, &cycle); err != nil {
		return cycle, err
	}
	if err := s.Kernel.SetLatestCycle(ctx, cycle.Portfolio, cycle.ID); err != nil {
		return cycle, fmt.Errorf("set latest cycle: %w", err)
	}
	if err := s.ensureCycleRun(ctx, &cycle); err != nil {
		return cycle, err
	}
	return s.continueObservation(ctx, &cycle)
}

func (s *Service) ResumeProposal(ctx context.Context, cycleID string) (cycle domain.Cycle, err error) {
	if err := s.validate(); err != nil {
		return domain.Cycle{}, err
	}
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	owner := "remontoire:" + cycleID
	if err := s.Kernel.AcquireCycleLock(ctx, cycle.Portfolio, owner, s.Config.LockTimeout); err != nil {
		return domain.Cycle{}, err
	}
	defer func() {
		cleanupCtx, cancel := boundedCleanupContext(ctx)
		defer cancel()
		releaseErr := s.Kernel.ReleaseCycleLock(cleanupCtx, cycle.Portfolio, owner)
		if err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	if err := s.ensureStageEvent(ctx, &cycle); err != nil {
		return cycle, err
	}
	if cycle.Stage == domain.StageAwaitingApproval || cycle.Stage == domain.StageCompleted {
		return cycle, nil
	}
	if cycle.Stage != domain.StageRanked && cycle.Stage != domain.StageProposed {
		return cycle, fmt.Errorf("cycle %s cannot resume proposal from stage %s", cycle.ID, cycle.Stage)
	}
	return s.ensureProposal(ctx, &cycle)
}

func (s *Service) validate() error {
	if s.Kernel == nil || s.Backlog == nil || s.Policy == nil || s.Judge == nil {
		return fmt.Errorf("kernel, backlog, policy, and judge are required")
	}
	if strings.TrimSpace(s.Config.Portfolio) == "" || !filepath.IsAbs(s.Config.ProjectDir) || !filepath.IsAbs(s.Config.ArtifactRoot) {
		return fmt.Errorf("portfolio and absolute project/artifact paths are required")
	}
	if len(s.Config.AllowedRepositoryRoots) == 0 {
		return fmt.Errorf("at least one allowed repository root is required")
	}
	for _, root := range s.Config.AllowedRepositoryRoots {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("allowed repository root %q must be absolute", root)
		}
	}
	if s.Config.DiscoveryLimit <= 0 {
		s.Config.DiscoveryLimit = 100
	}
	if s.Config.MaxInputBytes <= 0 {
		s.Config.MaxInputBytes = 1 << 20
	}
	if s.Config.LockTimeout == "" {
		s.Config.LockTimeout = "0s"
	}
	if s.Store.Root == "" {
		s.Store.Root = s.Config.ArtifactRoot
	}
	return nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) newID(now time.Time) (string, error) {
	if s.NewID != nil {
		return s.NewID(now)
	}
	return NewCycleID(now)
}

func NewCycleID(now time.Time) (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "cycle-" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random), nil
}
