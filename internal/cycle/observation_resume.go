package cycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
)

var ErrObservationIndeterminate = errors.New("observation capture is indeterminate and will not be repeated")

func (s *Service) ResumeObservation(ctx context.Context, cycleID string) (cycle domain.Cycle, err error) {
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
	if err := s.ensureCycleRun(ctx, &cycle); err != nil {
		return cycle, err
	}
	return s.continueObservation(ctx, &cycle)
}

func (s *Service) ensureCycleRun(ctx context.Context, cycle *domain.Cycle) error {
	if cycle.RunID != "" {
		return nil
	}
	runID, err := s.Kernel.CreateCycleRun(ctx, s.Config.ProjectDir, cycle.ID, map[string]any{
		"mode": cycle.Mode, "portfolio": cycle.Portfolio,
	})
	if err != nil {
		return fmt.Errorf("create intercore run: %w", err)
	}
	cycle.RunID = runID
	return s.persist(ctx, cycle)
}

func (s *Service) continueObservation(ctx context.Context, cycle *domain.Cycle) (domain.Cycle, error) {
	if cycle.RunID == "" {
		return *cycle, fmt.Errorf("cycle %s has no intercore run", cycle.ID)
	}
	if cycle.Stage == domain.StageRanked {
		if err := s.ensureStageEvent(ctx, cycle); err != nil {
			return *cycle, err
		}
		return s.finishRanked(ctx, cycle)
	}
	if cycle.Stage != domain.StageNew && cycle.Stage != domain.StageObserving {
		return *cycle, fmt.Errorf("cycle %s cannot resume observation from stage %s", cycle.ID, cycle.Stage)
	}
	if err := s.Kernel.SetLatestCycle(ctx, cycle.Portfolio, cycle.ID); err != nil {
		return *cycle, fmt.Errorf("set latest cycle: %w", err)
	}
	if cycle.Stage == domain.StageNew {
		if err := s.transition(ctx, cycle, domain.StageObserving); err != nil {
			return *cycle, err
		}
	} else if err := s.ensureStageEvent(ctx, cycle); err != nil {
		return *cycle, err
	}

	observation, observationJSON, err := s.loadOrCaptureObservation(ctx, cycle)
	if err != nil {
		return *cycle, s.fail(ctx, cycle, err)
	}
	judgment, judgmentArtifact, err := s.loadOrRunJudgment(ctx, *cycle, observation, observationJSON)
	if err != nil {
		return *cycle, s.fail(ctx, cycle, err)
	}
	appendArtifact(cycle, judgmentArtifact)
	cycle.Judgment = &judgment
	cycle.Candidate = nil
	cycle.CandidateHash = ""
	cycle.ContractHash = ""
	if judgment.SelectedIndex != nil {
		candidate := judgment.Opportunities[*judgment.SelectedIndex]
		if err := s.validateRepository(candidate.Contract.Repository); err != nil {
			return *cycle, s.fail(ctx, cycle, err)
		}
		contractHash, err := domain.HashContract(candidate.Contract)
		if err != nil {
			return *cycle, s.fail(ctx, cycle, err)
		}
		cycle.Candidate = &candidate
		cycle.CandidateHash = domain.FingerprintCandidate(candidate)
		cycle.ContractHash = contractHash
	}
	if err := s.persist(ctx, cycle); err != nil {
		return *cycle, err
	}
	if err := s.advanceOnce(ctx, cycle, "run:rank", "observe", "rank"); err != nil {
		return *cycle, s.fail(ctx, cycle, err)
	}
	if err := s.transition(ctx, cycle, domain.StageRanked); err != nil {
		return *cycle, err
	}
	return s.finishRanked(ctx, cycle)
}

func (s *Service) finishRanked(ctx context.Context, cycle *domain.Cycle) (domain.Cycle, error) {
	if cycle.Judgment == nil {
		return *cycle, fmt.Errorf("ranked cycle %s has no canonical judgment", cycle.ID)
	}
	if cycle.Judgment.SelectedIndex == nil {
		return s.completeNoOp(ctx, cycle, cycle.Judgment.NoOpReason)
	}
	if cycle.Mode == domain.ModeShadow {
		return s.completeNoOp(ctx, cycle, NoOpReasonShadowMode)
	}
	return s.ensureProposal(ctx, cycle)
}

func (s *Service) loadOrCaptureObservation(ctx context.Context, cycle *domain.Cycle) (Observation, []byte, error) {
	if cycle.IdempotencyKeys == nil {
		cycle.IdempotencyKeys = map[string]string{}
	}
	path, err := s.Store.Path(cycle.ID, "observation.json")
	if err != nil {
		return Observation{}, nil, err
	}
	var observation Observation
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		if err := json.Unmarshal(data, &observation); err != nil {
			return Observation{}, nil, fmt.Errorf("decode stored observation: %w", err)
		}
		if err := s.validateStoredObservation(*cycle, observation); err != nil {
			return Observation{}, nil, err
		}
	} else if os.IsNotExist(readErr) {
		if hasArtifactKind(cycle.Artifacts, "observation") {
			return Observation{}, nil, fmt.Errorf("canonical observation artifact is missing")
		}
		if cycle.IdempotencyKeys["observation:capture"] == "started" {
			return Observation{}, nil, ErrObservationIndeterminate
		}
		cycle.IdempotencyKeys["observation:capture"] = "started"
		if err := s.persist(ctx, cycle); err != nil {
			return Observation{}, nil, err
		}
		observation, err = s.observe(ctx, *cycle)
		if err != nil {
			return Observation{}, nil, err
		}
		data, err = json.Marshal(observation)
		if err != nil {
			return Observation{}, nil, err
		}
		if _, err := s.Store.WriteBytes(cycle.ID, "observation", "observation.json", append(data, '\n')); err != nil {
			return Observation{}, nil, err
		}
	} else {
		return Observation{}, nil, fmt.Errorf("read stored observation: %w", readErr)
	}
	canonical, err := json.Marshal(observation)
	if err != nil {
		return Observation{}, nil, err
	}
	for _, artifact := range observation.Artifacts {
		if err := ensureCanonicalArtifactBinding(*cycle, artifact); err != nil {
			return Observation{}, nil, err
		}
		appendArtifact(cycle, artifact)
	}
	observationArtifact, err := s.Store.HashExisting("observation", path)
	if err != nil {
		return Observation{}, nil, err
	}
	if err := ensureCanonicalArtifactBinding(*cycle, observationArtifact); err != nil {
		return Observation{}, nil, err
	}
	appendArtifact(cycle, observationArtifact)
	switch cycle.IdempotencyKeys["replay:observation"] {
	case observationArtifact.Digest:
	case "started":
		return Observation{}, nil, ErrObservationIndeterminate
	case "":
		cycle.IdempotencyKeys["replay:observation"] = "started"
		if err := s.persist(ctx, cycle); err != nil {
			return Observation{}, nil, err
		}
		payload, _ := json.Marshal(map[string]string{"sha256": observationArtifact.Digest})
		if err := s.Kernel.RecordReplayInput(ctx, cycle.RunID, observationArtifact.Kind, "observation.json", string(payload), observationArtifact.Path); err != nil {
			return Observation{}, nil, fmt.Errorf("register composite observation: %w", err)
		}
		cycle.IdempotencyKeys["replay:observation"] = observationArtifact.Digest
	default:
		return Observation{}, nil, fmt.Errorf("canonical observation replay digest does not match stored observation")
	}
	cycle.IdempotencyKeys["observation:capture"] = "completed"
	if err := s.persist(ctx, cycle); err != nil {
		return Observation{}, nil, err
	}
	return observation, canonical, nil
}

func ensureCanonicalArtifactBinding(cycle domain.Cycle, artifact domain.Artifact) error {
	matches := 0
	for _, existing := range cycle.Artifacts {
		if existing.Kind != artifact.Kind {
			continue
		}
		matches++
		if existing.Path != artifact.Path || existing.Digest != artifact.Digest {
			return fmt.Errorf("%s artifact digest changed from canonical state", artifact.Kind)
		}
	}
	if matches > 1 {
		return fmt.Errorf("canonical %s artifact is ambiguous", artifact.Kind)
	}
	return nil
}

func (s *Service) validateStoredObservation(cycle domain.Cycle, observation Observation) error {
	if observation.SchemaVersion != ObservationSchemaV1 || observation.CycleID != cycle.ID || observation.Portfolio != cycle.Portfolio {
		return fmt.Errorf("stored observation identity or schema is invalid")
	}
	cycleDir, err := s.Store.CycleDir(cycle.ID)
	if err != nil {
		return err
	}
	required := map[string]bool{"beads": false, "discoveries": false, "interest-profile": false, "ockham": false, "outcomes": false}
	seen := map[string]bool{}
	for _, artifact := range observation.Artifacts {
		if seen[artifact.Kind] {
			return fmt.Errorf("stored observation artifact %s is duplicated", artifact.Kind)
		}
		seen[artifact.Kind] = true
		cleaned := filepath.Clean(artifact.Path)
		rel, relErr := filepath.Rel(cycleDir, cleaned)
		if artifact.Path != cleaned || !filepath.IsAbs(cleaned) || relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("stored observation artifact %s has an unsafe path", artifact.Kind)
		}
		current, err := s.Store.HashExisting(artifact.Kind, artifact.Path)
		if err != nil {
			return fmt.Errorf("hash stored observation artifact %s: %w", artifact.Kind, err)
		}
		if current.Digest != artifact.Digest {
			return fmt.Errorf("stored observation artifact %s digest changed", artifact.Kind)
		}
		if _, ok := required[artifact.Kind]; ok {
			required[artifact.Kind] = true
		} else if artifact.Kind != "roadmap" {
			return fmt.Errorf("stored observation artifact kind %s is invalid", artifact.Kind)
		}
	}
	for kind, present := range required {
		if !present {
			return fmt.Errorf("stored observation artifact %s is missing", kind)
		}
	}
	return nil
}

func (s *Service) loadOrRunJudgment(ctx context.Context, cycle domain.Cycle, observation Observation, observationJSON []byte) (domain.Judgment, domain.Artifact, error) {
	path, err := s.Store.Path(cycle.ID, "judgment.json")
	if err != nil {
		return domain.Judgment{}, domain.Artifact{}, err
	}
	var judgment domain.Judgment
	loadedFromDisk := false
	if data, readErr := os.ReadFile(path); readErr == nil {
		loadedFromDisk = true
		if err := json.Unmarshal(data, &judgment); err != nil {
			return domain.Judgment{}, domain.Artifact{}, fmt.Errorf("decode stored judgment: %w", err)
		}
	} else if os.IsNotExist(readErr) {
		if cycle.Judgment != nil || hasArtifactKind(cycle.Artifacts, "judgment") {
			return domain.Judgment{}, domain.Artifact{}, fmt.Errorf("canonical judgment artifact is missing")
		}
		judgment, _, err = s.Judge.Judge(ctx, harness.JudgmentRequest{
			WorkingDir: s.Config.ProjectDir, SchemaPath: s.Config.JudgmentSchemaPath,
			OutputPath: path, Observation: observationJSON, MaxInputBytes: s.Config.MaxInputBytes,
		})
		if err != nil {
			return domain.Judgment{}, domain.Artifact{}, fmt.Errorf("portfolio judgment: %w", err)
		}
	} else {
		return domain.Judgment{}, domain.Artifact{}, fmt.Errorf("read stored judgment: %w", readErr)
	}
	if loadedFromDisk {
		current, err := s.Store.HashExisting("judgment", path)
		if err != nil {
			return domain.Judgment{}, domain.Artifact{}, err
		}
		if err := ensureCanonicalArtifactBinding(cycle, current); err != nil {
			return domain.Judgment{}, domain.Artifact{}, err
		}
	}
	if err := domain.ValidateJudgment(judgment); err != nil {
		return domain.Judgment{}, domain.Artifact{}, fmt.Errorf("portfolio judgment: %w", err)
	}
	if err := validateEvidenceBindings(judgment, observation); err != nil {
		return domain.Judgment{}, domain.Artifact{}, err
	}
	if err := validateSelectedRanking(judgment); err != nil {
		return domain.Judgment{}, domain.Artifact{}, err
	}
	if cycle.Judgment != nil {
		left, _ := json.Marshal(*cycle.Judgment)
		right, _ := json.Marshal(judgment)
		if string(left) != string(right) {
			return domain.Judgment{}, domain.Artifact{}, fmt.Errorf("canonical judgment does not match stored judgment")
		}
	}
	artifact, err := s.Store.WriteJSON(cycle.ID, "judgment", "judgment.json", judgment)
	if err != nil {
		return domain.Judgment{}, domain.Artifact{}, err
	}
	if err := ensureCanonicalArtifactBinding(cycle, artifact); err != nil {
		return domain.Judgment{}, domain.Artifact{}, err
	}
	return judgment, artifact, nil
}

func hasArtifactKind(artifacts []domain.Artifact, kind string) bool {
	for _, artifact := range artifacts {
		if artifact.Kind == kind {
			return true
		}
	}
	return false
}
