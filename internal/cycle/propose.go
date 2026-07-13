package cycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/strictjson"
)

const (
	NoOpReasonDuplicateFingerprint = "duplicate fingerprint already exists in canonical backlog"
	NoOpReasonShadowMode           = "shadow mode: proposal recorded without backlog mutation"
)

var ErrProposalPreflightIndeterminate = errors.New("proposal backlog replay capture is indeterminate")

func (s *Service) ensureProposal(ctx context.Context, cycle *domain.Cycle) (domain.Cycle, error) {
	if cycle.Candidate == nil || cycle.CandidateHash == "" || cycle.ContractHash == "" {
		return *cycle, fmt.Errorf("cycle %s has no validated candidate", cycle.ID)
	}
	if snapshot, found, err := s.loadProposalBacklogSnapshot(ctx, cycle); err != nil {
		return *cycle, err
	} else if found {
		if !adapters.HasFingerprint(snapshot, cycle.CandidateHash) {
			return *cycle, fmt.Errorf("proposal backlog snapshot does not contain the duplicate fingerprint")
		}
		return s.completeNoOp(ctx, cycle, NoOpReasonDuplicateFingerprint)
	}
	beads, err := s.Backlog.List(ctx)
	if err != nil {
		return *cycle, fmt.Errorf("deduplicate proposal: %w", err)
	}
	if existing, ok := adapters.FindCycleExperiment(beads, cycle.ID); ok {
		cycle.ExperimentBeadID = existing.ID
		cycle.IdempotencyKeys["bead:create"] = existing.ID
		if cycle.Stage == domain.StageRanked {
			if err := s.advanceOnce(ctx, cycle, "run:propose", "rank", "propose"); err != nil {
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
		if _, err := s.captureProposalBacklogSnapshot(ctx, cycle, beads); err != nil {
			return *cycle, err
		}
		return s.completeNoOp(ctx, cycle, NoOpReasonDuplicateFingerprint)
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
	if err := s.advanceOnce(ctx, cycle, "run:propose", "rank", "propose"); err != nil {
		return *cycle, err
	}
	if err := s.transition(ctx, cycle, domain.StageProposed); err != nil {
		return *cycle, err
	}
	if err := s.transition(ctx, cycle, domain.StageAwaitingApproval); err != nil {
		return *cycle, err
	}
	return *cycle, nil
}

func (s *Service) loadProposalBacklogSnapshot(ctx context.Context, cycle *domain.Cycle) ([]adapters.Bead, bool, error) {
	path, err := s.Store.Path(cycle.ID, "proposal-backlog.json")
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read proposal backlog snapshot: %w", err)
	}
	beads, artifact, err := s.validateProposalBacklogSnapshot(*cycle, path, data)
	if err != nil {
		return nil, false, err
	}
	appendArtifact(cycle, artifact)
	if err := s.persist(ctx, cycle); err != nil {
		return nil, false, err
	}
	if err := s.registerProposalBacklogSnapshot(ctx, cycle, artifact); err != nil {
		return nil, false, err
	}
	return beads, true, nil
}

func (s *Service) captureProposalBacklogSnapshot(ctx context.Context, cycle *domain.Cycle, beads []adapters.Bead) (domain.Artifact, error) {
	artifact, err := s.Store.WriteJSON(cycle.ID, "proposal-backlog", "proposal-backlog.json", beads)
	if err != nil {
		return domain.Artifact{}, fmt.Errorf("snapshot proposal backlog: %w", err)
	}
	if err := ensureCanonicalArtifactBinding(*cycle, artifact); err != nil {
		return domain.Artifact{}, err
	}
	appendArtifact(cycle, artifact)
	if err := s.persist(ctx, cycle); err != nil {
		return domain.Artifact{}, err
	}
	if err := s.registerProposalBacklogSnapshot(ctx, cycle, artifact); err != nil {
		return domain.Artifact{}, err
	}
	return artifact, nil
}

func (s *Service) validateProposalBacklogSnapshot(cycle domain.Cycle, path string, data []byte) ([]adapters.Bead, domain.Artifact, error) {
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		return nil, domain.Artifact{}, fmt.Errorf("decode proposal backlog snapshot: %w", err)
	}
	var beads []adapters.Bead
	if err := json.Unmarshal(data, &beads); err != nil {
		return nil, domain.Artifact{}, fmt.Errorf("decode proposal backlog snapshot: %w", err)
	}
	sum := sha256.Sum256(data)
	artifact := domain.Artifact{Kind: "proposal-backlog", Path: path, Digest: hex.EncodeToString(sum[:])}
	if err := ensureCanonicalArtifactBinding(cycle, artifact); err != nil {
		return nil, domain.Artifact{}, err
	}
	return beads, artifact, nil
}

func (s *Service) registerProposalBacklogSnapshot(ctx context.Context, cycle *domain.Cycle, artifact domain.Artifact) error {
	const key = "replay:proposal-backlog"
	switch cycle.IdempotencyKeys[key] {
	case artifact.Digest:
		return nil
	case "started":
		return ErrProposalPreflightIndeterminate
	case "":
	default:
		return fmt.Errorf("canonical proposal backlog replay digest does not match stored snapshot")
	}
	cycle.IdempotencyKeys[key] = "started"
	if err := s.persist(ctx, cycle); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"sha256": artifact.Digest})
	if err := s.Kernel.RecordReplayInput(ctx, cycle.RunID, artifact.Kind, filepathBase(artifact.Path), string(payload), artifact.Path); err != nil {
		return fmt.Errorf("%w: %v", ErrProposalPreflightIndeterminate, err)
	}
	cycle.IdempotencyKeys[key] = artifact.Digest
	return s.persist(ctx, cycle)
}

func (s *Service) completeNoOp(ctx context.Context, cycle *domain.Cycle, reason string) (domain.Cycle, error) {
	cycle.NoOpReason = reason
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
	return s.ensureStageEvent(ctx, cycle)
}

func (s *Service) ensureStageEvent(ctx context.Context, cycle *domain.Cycle) error {
	if cycle.Stage == domain.StageNew {
		return nil
	}
	if cycle.IdempotencyKeys == nil {
		cycle.IdempotencyKeys = map[string]string{}
	}
	key := "event:" + string(cycle.Stage)
	if cycle.IdempotencyKeys[key] == "recorded" {
		return nil
	}
	if err := s.Kernel.RecordStageEvent(ctx, cycle.RunID, s.Config.ProjectDir, cycle.Stage, cycle.ID); err != nil {
		return fmt.Errorf("record %s event: %w", cycle.Stage, err)
	}
	cycle.IdempotencyKeys[key] = "recorded"
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
		cleanupCtx, cancel := boundedCleanupContext(ctx)
		defer cancel()
		if err := s.transition(cleanupCtx, cycle, domain.StageFailed); err != nil {
			return errors.Join(cause, fmt.Errorf("persist failed cycle: %w", err))
		}
	}
	return cause
}

func validateEvidenceBindings(judgment domain.Judgment, observation Observation) error {
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
	outcomeIDs := map[string]bool{}
	for _, outcome := range observation.PriorOutcomes {
		outcomeIDs[outcome.CycleID] = true
	}
	for opportunityIndex, candidate := range judgment.Opportunities {
		for _, ref := range candidate.Evidence {
			var artifactKind string
			switch ref.Kind {
			case "bead":
				artifactKind = "beads"
				if !beadIDs[ref.ID] {
					return fmt.Errorf("opportunity %d evidence bead %q is not in the observation", opportunityIndex, ref.ID)
				}
			case "discovery":
				artifactKind = "discoveries"
				if !discoveryIDs[ref.ID] {
					return fmt.Errorf("opportunity %d evidence discovery %q is not in the observation", opportunityIndex, ref.ID)
				}
			case "policy":
				artifactKind = "ockham"
			case "roadmap":
				artifactKind = "roadmap"
			case "outcome":
				artifactKind = "outcomes"
				if !outcomeIDs[ref.ID] {
					return fmt.Errorf("opportunity %d evidence outcome %q is not in the observation", opportunityIndex, ref.ID)
				}
			default:
				return fmt.Errorf("opportunity %d evidence kind %q is not bound to a canonical input", opportunityIndex, ref.Kind)
			}
			if digestByKind[artifactKind] == "" || digestByKind[artifactKind] != ref.Digest {
				return fmt.Errorf("opportunity %d evidence %s:%s digest does not match canonical input", opportunityIndex, ref.Kind, ref.ID)
			}
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
	_, err := s.resolveRepository(repository)
	return err
}

func (s *Service) resolveRepository(repository string) (string, error) {
	cleaned := filepath.Clean(repository)
	if !filepath.IsAbs(cleaned) || cleaned != repository {
		return "", fmt.Errorf("candidate repository must be a clean absolute path")
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve candidate repository: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat candidate repository: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("candidate repository must be a directory")
	}
	for _, root := range s.Config.AllowedRepositoryRoots {
		resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			return "", fmt.Errorf("resolve allowed repository root %q: %w", root, err)
		}
		rel, err := filepath.Rel(resolvedRoot, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("candidate repository %q is outside allowed repository roots", repository)
}
