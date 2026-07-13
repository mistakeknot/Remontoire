package cycle

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

func (s *Service) ensureProposal(ctx context.Context, cycle *domain.Cycle) (domain.Cycle, error) {
	if cycle.Candidate == nil || cycle.CandidateHash == "" || cycle.ContractHash == "" {
		return *cycle, fmt.Errorf("cycle %s has no validated candidate", cycle.ID)
	}
	beads, err := s.Backlog.List(ctx)
	if err != nil {
		return *cycle, fmt.Errorf("deduplicate proposal: %w", err)
	}
	if existing, ok := adapters.FindCycleExperiment(beads, cycle.ID); ok {
		cycle.ExperimentBeadID = existing.ID
		cycle.IdempotencyKeys["bead:create"] = existing.ID
		if cycle.Stage == domain.StageRanked {
			if err := s.Kernel.AdvanceRun(ctx, cycle.RunID); err != nil {
				return *cycle, err
			}
			if err := s.transition(ctx, cycle, domain.StageProposed); err != nil {
				return *cycle, err
			}
		}
		if cycle.Stage == domain.StageProposed {
			if err := s.transition(ctx, cycle, domain.StageAwaitingApproval); err != nil {
				return *cycle, err
			}
		}
		return *cycle, nil
	}
	if adapters.HasFingerprint(beads, cycle.CandidateHash) {
		cycle.Judgment.SelectedIndex = nil
		cycle.Judgment.NoOpReason = "duplicate fingerprint already exists in canonical backlog"
		return s.completeNoOp(ctx, cycle, cycle.Judgment.NoOpReason)
	}
	if cycle.Stage != domain.StageRanked {
		return *cycle, fmt.Errorf("cannot create proposal from stage %s", cycle.Stage)
	}
	beadID, err := s.Backlog.CreateExperiment(ctx, cycle.ID, cycle.CandidateHash, *cycle.Candidate)
	if err != nil {
		return *cycle, fmt.Errorf("create experiment bead: %w", err)
	}
	cycle.ExperimentBeadID = beadID
	cycle.IdempotencyKeys["bead:create"] = beadID
	if err := s.Kernel.AdvanceRun(ctx, cycle.RunID); err != nil {
		return *cycle, fmt.Errorf("advance run to propose: %w", err)
	}
	if err := s.transition(ctx, cycle, domain.StageProposed); err != nil {
		return *cycle, err
	}
	if err := s.transition(ctx, cycle, domain.StageAwaitingApproval); err != nil {
		return *cycle, err
	}
	return *cycle, nil
}

func (s *Service) completeNoOp(ctx context.Context, cycle *domain.Cycle, reason string) (domain.Cycle, error) {
	if cycle.Judgment != nil && cycle.Judgment.SelectedIndex == nil && cycle.Judgment.NoOpReason == "" {
		cycle.Judgment.NoOpReason = reason
	}
	if err := s.transition(ctx, cycle, domain.StageNoOp); err != nil {
		return *cycle, err
	}
	if err := s.transition(ctx, cycle, domain.StageCompleted); err != nil {
		return *cycle, err
	}
	return *cycle, nil
}

func (s *Service) transition(ctx context.Context, cycle *domain.Cycle, next domain.Stage) error {
	if err := domain.ValidateTransition(cycle.Stage, next); err != nil {
		return err
	}
	cycle.Stage = next
	cycle.UpdatedAt = s.now()
	if err := s.persist(ctx, cycle); err != nil {
		return err
	}
	if err := s.Kernel.RecordStageEvent(ctx, cycle.RunID, s.Config.ProjectDir, next, cycle.ID); err != nil {
		return fmt.Errorf("record %s event: %w", next, err)
	}
	cycle.IdempotencyKeys["event:"+string(next)] = "recorded"
	return s.persist(ctx, cycle)
}

func (s *Service) persist(ctx context.Context, cycle *domain.Cycle) error {
	if cycle.IdempotencyKeys == nil {
		cycle.IdempotencyKeys = map[string]string{}
	}
	if err := s.Kernel.SetCycle(ctx, *cycle); err != nil {
		return fmt.Errorf("persist canonical cycle state: %w", err)
	}
	if _, err := s.Store.WriteCycle(*cycle); err != nil {
		return fmt.Errorf("write cycle projection: %w", err)
	}
	return nil
}

func (s *Service) fail(ctx context.Context, cycle *domain.Cycle, cause error) error {
	cycle.Failure = cause.Error()
	if cycle.Stage != domain.StageFailed && cycle.Stage != domain.StageCompleted {
		if err := s.transition(ctx, cycle, domain.StageFailed); err != nil {
			return fmt.Errorf("%v; additionally failed to persist failure: %w", cause, err)
		}
	}
	return cause
}

func validateEvidenceBindings(judgment domain.Judgment, observation Observation) error {
	if judgment.SelectedIndex == nil {
		return nil
	}
	digestByKind := map[string]string{}
	for _, artifact := range observation.Artifacts {
		digestByKind[artifact.Kind] = artifact.Digest
	}
	beadIDs := map[string]bool{}
	for _, bead := range observation.Beads {
		beadIDs[bead.ID] = true
	}
	discoveryIDs := map[string]bool{}
	for _, discovery := range observation.Discoveries {
		discoveryIDs[discovery.ID] = true
	}
	candidate := judgment.Opportunities[*judgment.SelectedIndex]
	for _, ref := range candidate.Evidence {
		var artifactKind string
		switch ref.Kind {
		case "bead":
			artifactKind = "beads"
			if !beadIDs[ref.ID] {
				return fmt.Errorf("evidence bead %q is not in the observation", ref.ID)
			}
		case "discovery":
			artifactKind = "discoveries"
			if !discoveryIDs[ref.ID] {
				return fmt.Errorf("evidence discovery %q is not in the observation", ref.ID)
			}
		case "policy":
			artifactKind = "ockham"
		case "roadmap":
			artifactKind = "roadmap"
		default:
			return fmt.Errorf("evidence kind %q is not bound to a canonical input", ref.Kind)
		}
		if digestByKind[artifactKind] == "" || digestByKind[artifactKind] != ref.Digest {
			return fmt.Errorf("evidence %s:%s digest does not match canonical input", ref.Kind, ref.ID)
		}
	}
	return nil
}

func validateSelectedRanking(judgment domain.Judgment) error {
	if judgment.SelectedIndex == nil {
		return nil
	}
	selected := leverageScore(judgment.Opportunities[*judgment.SelectedIndex])
	for i, candidate := range judgment.Opportunities {
		if i != *judgment.SelectedIndex && leverageScore(candidate) > selected+1e-9 {
			return fmt.Errorf("selected opportunity is not the highest leverage score")
		}
	}
	return nil
}

func leverageScore(candidate domain.Candidate) float64 {
	return 0.30*candidate.Impact + 0.30*candidate.Uncertainty + 0.20*candidate.PolicyFit - 0.10*candidate.Cost - 0.10*candidate.Risk
}

func (s *Service) validateRepository(repository string) error {
	cleaned := filepath.Clean(repository)
	if !filepath.IsAbs(cleaned) {
		return fmt.Errorf("candidate repository must be absolute")
	}
	for _, root := range s.Config.AllowedRepositoryRoots {
		rel, err := filepath.Rel(filepath.Clean(root), cleaned)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return nil
		}
	}
	return fmt.Errorf("candidate repository %q is outside allowed repository roots", repository)
}
