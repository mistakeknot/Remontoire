package cycle

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/harness"
	receiptpkg "github.com/mistakeknot/Remontoire/internal/receipt"
)

const ReviewMaterialSchemaV1 = "remontoire.review-material/v1"

var (
	ErrReviewIndeterminate  = errors.New("review attempt is indeterminate and will not be repeated")
	ErrReceiptIndeterminate = errors.New("receipt emission is indeterminate and will not be repeated")
	ErrAdvanceIndeterminate = errors.New("run advance is indeterminate and will not be repeated")
)

type ReviewMaterial struct {
	SchemaVersion    string                 `json:"schema_version"`
	CycleID          string                 `json:"cycle_id"`
	ContractHash     string                 `json:"contract_hash"`
	Candidate        domain.Candidate       `json:"candidate"`
	Execution        domain.ExecutionRecord `json:"execution"`
	Measurement      domain.Measurement     `json:"measurement"`
	Artifacts        []domain.Artifact      `json:"artifacts"`
	ArtifactContents map[string]string      `json:"artifact_contents"`
}

type receiptSignature struct {
	ReceiptID   string `json:"receipt_id"`
	ContentHash string `json:"content_hash"`
}

func (s *Service) Review(ctx context.Context, cycleID string) (cycle domain.Cycle, err error) {
	if err := s.validateReview(); err != nil {
		return domain.Cycle{}, err
	}
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	if cycle.Stage != domain.StageReviewing && !(cycle.Stage == domain.StageCompounding && cycle.Resolution != nil) {
		return cycle, fmt.Errorf("cycle %s is not ready for review (stage %s)", cycle.ID, cycle.Stage)
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
	if cycle.Stage == domain.StageCompounding && cycle.Resolution != nil {
		return cycle, nil
	}
	if cycle.Candidate == nil || cycle.Execution == nil || cycle.Measurement == nil {
		return cycle, fmt.Errorf("cycle %s is missing execution evidence", cycle.ID)
	}
	if err := s.validateReviewProjection(cycle); err != nil {
		return cycle, s.fail(ctx, &cycle, err)
	}

	backendName, reviewer, err := s.selectReviewer(cycle)
	if err != nil {
		return cycle, err
	}
	outputPath, err := s.Store.Path(cycle.ID, "review.json")
	if err != nil {
		return cycle, err
	}

	var review domain.Review
	var metadata harness.Metadata
	if cycle.IdempotencyKeys["review:attempt"] == "completed" && cycle.Review != nil && cycle.Resolution != nil {
		review = *cycle.Review
	} else if cycle.IdempotencyKeys["review:attempt"] == "started" {
		if _, statErr := os.Stat(outputPath); statErr != nil {
			if os.IsNotExist(statErr) {
				return cycle, ErrReviewIndeterminate
			}
			return cycle, statErr
		}
		review, err = harness.LoadReview(outputPath, cycle.ContractHash)
		if err != nil {
			return cycle, fmt.Errorf("recover review: %w", err)
		}
		artifact, hashErr := s.Store.HashExisting("review", outputPath)
		if hashErr != nil {
			return cycle, hashErr
		}
		appendArtifact(&cycle, artifact)
		metadata = harness.Metadata{Backend: backendName}
	} else {
		material, data, buildErr := s.buildReviewMaterial(cycle)
		if buildErr != nil {
			return cycle, s.fail(ctx, &cycle, buildErr)
		}
		materialArtifact, writeErr := s.Store.WriteJSON(cycle.ID, "review-material", "review-material.json", material)
		if writeErr != nil {
			return cycle, s.fail(ctx, &cycle, writeErr)
		}
		appendArtifact(&cycle, materialArtifact)
		cycle.IdempotencyKeys["review:attempt"] = "started"
		if err := s.persist(ctx, &cycle); err != nil {
			return cycle, err
		}
		review, metadata, err = reviewer.Review(ctx, harness.ReviewRequest{
			WorkingDir: cycle.Execution.WorktreePath, SchemaPath: s.Config.ReviewSchemaPath,
			OutputPath: outputPath, Contract: cycle.Candidate.Contract, ContractHash: cycle.ContractHash,
			Material: data, MaxInputBytes: s.Config.MaxInputBytes, MaxBudgetUSD: 1,
		})
		if metadataErr := s.retainReviewerMetadata(&cycle, metadata); metadataErr != nil {
			return cycle, s.fail(ctx, &cycle, metadataErr)
		}
		if err != nil {
			return cycle, s.fail(ctx, &cycle, fmt.Errorf("independent review: %w", err))
		}
		if err := harness.ValidateReview(review, cycle.ContractHash); err != nil {
			return cycle, s.fail(ctx, &cycle, fmt.Errorf("independent review: %w", err))
		}
		artifact, writeErr := s.Store.WriteJSON(cycle.ID, "review", "review.json", review)
		if writeErr != nil {
			return cycle, s.fail(ctx, &cycle, writeErr)
		}
		appendArtifact(&cycle, artifact)
	}

	if err := s.validateReviewEvidence(review, cycle.Artifacts); err != nil {
		return cycle, s.fail(ctx, &cycle, err)
	}
	cycle.Review = &review
	if cycle.Resolution == nil {
		resolution := domain.ReviewResolution{
			ReviewerVerdict: review.Verdict, FinalVerdict: review.Verdict,
			ReviewerBackend: metadata.Backend, ReviewerModel: metadata.Model, ResolvedAt: s.now(),
		}
		if review.Verdict == domain.VerdictPromote {
			if promoteErr := domain.ValidatePromotion(cycle.Candidate.Contract, *cycle.Measurement, review); promoteErr != nil {
				resolution.FinalVerdict = domain.VerdictCloseFailure
				resolution.OverrideReason = "deterministic promotion veto: " + promoteErr.Error()
			}
		}
		cycle.Resolution = &resolution
	}
	cycle.IdempotencyKeys["review:attempt"] = "completed"
	if err := s.persist(ctx, &cycle); err != nil {
		return cycle, err
	}
	if err := s.advanceOnce(ctx, &cycle, "run:compound", "review", "compound"); err != nil {
		return cycle, err
	}
	if err := s.transition(ctx, &cycle, domain.StageCompounding); err != nil {
		return cycle, err
	}
	return cycle, nil
}

func (s *Service) retainReviewerMetadata(cycle *domain.Cycle, metadata harness.Metadata) error {
	for _, value := range []struct {
		kind string
		name string
		data []byte
	}{
		{kind: "reviewer-transcript", name: "reviewer.jsonl", data: metadata.Transcript},
		{kind: "reviewer-stderr", name: "reviewer.stderr", data: metadata.Stderr},
	} {
		if len(value.data) == 0 {
			continue
		}
		artifact, err := s.Store.WriteBytes(cycle.ID, value.kind, value.name, value.data)
		if err != nil {
			return err
		}
		appendArtifact(cycle, artifact)
	}
	return nil
}

func (s *Service) Compound(ctx context.Context, cycleID string) (cycle domain.Cycle, err error) {
	if err := s.validate(); err != nil {
		return domain.Cycle{}, err
	}
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	if cycle.Stage != domain.StageCompounding && cycle.Stage != domain.StageCompleted && cycle.Stage != domain.StageFailed && cycle.Stage != domain.StageNoOp {
		return cycle, fmt.Errorf("cycle %s is not ready to compound (stage %s)", cycle.ID, cycle.Stage)
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
	if cycle.Stage == domain.StageNoOp {
		if err := s.transition(ctx, &cycle, domain.StageCompleted); err != nil {
			return cycle, err
		}
	}
	if cycle.Stage == domain.StageCompleted && cycle.SignedReceiptID != "" {
		if err := s.terminalizeRun(ctx, &cycle); err != nil {
			return cycle, err
		}
		return cycle, nil
	}

	if cycle.Stage == domain.StageCompounding {
		if cycle.Candidate == nil || cycle.Measurement == nil || cycle.Review == nil || cycle.Resolution == nil || cycle.ExperimentBeadID == "" {
			return cycle, fmt.Errorf("cycle %s is missing reviewed experiment evidence", cycle.ID)
		}
		if !filepath.IsAbs(s.Config.RoadmapPath) {
			return cycle, fmt.Errorf("absolute roadmap path is required for compounding")
		}
		if s.Roadmap == nil {
			return cycle, fmt.Errorf("roadmap adapter is required")
		}
		if err := s.validateCompoundingEvidence(cycle); err != nil {
			return cycle, err
		}
		if err := s.compoundBacklog(ctx, &cycle); err != nil {
			return cycle, err
		}
		if err := s.compoundOutcome(ctx, &cycle); err != nil {
			return cycle, err
		}
		if err := s.compoundFeedback(ctx, &cycle); err != nil {
			return cycle, err
		}
		if cycle.IdempotencyKeys["roadmap:sync"] != "completed" {
			cycle.IdempotencyKeys["roadmap:sync"] = "started"
			if err := s.persist(ctx, &cycle); err != nil {
				return cycle, err
			}
			digest, syncErr := s.Roadmap.Sync(ctx)
			if syncErr != nil {
				delete(cycle.IdempotencyKeys, "roadmap:sync")
				cleanupCtx, cancel := boundedCleanupContext(ctx)
				_ = s.persist(cleanupCtx, &cycle)
				cancel()
				return cycle, fmt.Errorf("regenerate roadmap: %w", syncErr)
			}
			if !canonicalSHA256(digest) {
				return cycle, fmt.Errorf("roadmap digest must be a 64-character lowercase SHA-256 hex digest")
			}
			data, readErr := os.ReadFile(s.Config.RoadmapPath)
			if readErr != nil {
				return cycle, fmt.Errorf("read regenerated roadmap: %w", readErr)
			}
			artifact, writeErr := s.Store.WriteBytes(cycle.ID, "roadmap-output", "roadmap-output.json", data)
			if writeErr != nil {
				return cycle, fmt.Errorf("snapshot regenerated roadmap: %w", writeErr)
			}
			if digest != artifact.Digest {
				return cycle, fmt.Errorf("roadmap digest %q does not match generated artifact %q", digest, artifact.Digest)
			}
			appendArtifact(&cycle, artifact)
			cycle.RoadmapDigest = digest
			cycle.IdempotencyKeys["roadmap:sync"] = "completed"
			if err := s.persist(ctx, &cycle); err != nil {
				return cycle, err
			}
		}
		if err := s.validateRoadmapArtifact(cycle); err != nil {
			return cycle, err
		}
	}
	if err := s.terminalizeRun(ctx, &cycle); err != nil {
		return cycle, err
	}

	if err := s.finalizeReceipt(ctx, &cycle); err != nil {
		return cycle, err
	}
	if cycle.Stage == domain.StageCompounding {
		if err := s.transition(ctx, &cycle, domain.StageCompleted); err != nil {
			return cycle, err
		}
	}
	return cycle, nil
}

func (s *Service) validateReview() error {
	if err := s.validate(); err != nil {
		return err
	}
	if len(s.Reviewers) == 0 || s.Config.ReviewSchemaPath == "" {
		return fmt.Errorf("reviewers and review schema path are required")
	}
	return nil
}

func (s *Service) selectReviewer(cycle domain.Cycle) (string, Reviewer, error) {
	want := strings.ToLower(strings.TrimSpace(s.Config.ReviewerBackend))
	executor := ""
	if cycle.Execution != nil {
		executor = strings.ToLower(cycle.Execution.Backend)
	}
	names := make([]string, 0, len(s.Reviewers))
	for name := range s.Reviewers {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	if want != "" && want != executor {
		if reviewer, ok := s.Reviewers[want]; ok {
			return want, reviewer, nil
		}
	}
	for _, name := range names {
		if name != executor {
			return name, s.Reviewers[name], nil
		}
	}
	if want != "" {
		if reviewer, ok := s.Reviewers[want]; ok {
			return want, reviewer, nil
		}
	}
	if len(names) > 0 {
		return names[0], s.Reviewers[names[0]], nil
	}
	return "", nil, fmt.Errorf("no reviewer backend is configured")
}

func (s *Service) buildReviewMaterial(cycle domain.Cycle) (ReviewMaterial, []byte, error) {
	material := ReviewMaterial{
		SchemaVersion: ReviewMaterialSchemaV1, CycleID: cycle.ID, ContractHash: cycle.ContractHash,
		Candidate: *cycle.Candidate, Execution: *cycle.Execution, Measurement: *cycle.Measurement,
		Artifacts: append([]domain.Artifact(nil), cycle.Artifacts...), ArtifactContents: map[string]string{},
	}
	cycleDir, err := s.Store.CycleDir(cycle.ID)
	if err != nil {
		return ReviewMaterial{}, nil, err
	}
	include := map[string]bool{
		"patch": true, "execution-report": true, "executor-transcript": true,
		"benchmark-stdout": true, "benchmark-stderr": true, "measurement": true,
	}
	for _, artifact := range cycle.Artifacts {
		if !include[artifact.Kind] {
			continue
		}
		rel, relErr := filepath.Rel(cycleDir, artifact.Path)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ReviewMaterial{}, nil, fmt.Errorf("review artifact %s is outside the cycle directory", artifact.Kind)
		}
		current, hashErr := s.Store.HashExisting(artifact.Kind, artifact.Path)
		if hashErr != nil {
			return ReviewMaterial{}, nil, fmt.Errorf("hash review artifact %s: %w", artifact.Kind, hashErr)
		}
		if current.Digest != artifact.Digest {
			return ReviewMaterial{}, nil, fmt.Errorf("review artifact %s digest changed after capture", artifact.Kind)
		}
		data, readErr := os.ReadFile(artifact.Path)
		if readErr != nil {
			return ReviewMaterial{}, nil, fmt.Errorf("read review artifact %s: %w", artifact.Kind, readErr)
		}
		material.ArtifactContents[filepath.Base(artifact.Path)] = string(data)
	}
	data, err := json.Marshal(material)
	if err != nil {
		return ReviewMaterial{}, nil, fmt.Errorf("marshal review material: %w", err)
	}
	if s.Config.MaxInputBytes > 0 && len(data) > s.Config.MaxInputBytes {
		return ReviewMaterial{}, nil, fmt.Errorf("review material exceeds %d byte input limit", s.Config.MaxInputBytes)
	}
	return material, data, nil
}

func (s *Service) validateReviewProjection(cycle domain.Cycle) error {
	if cycle.Candidate == nil {
		return fmt.Errorf("review candidate is missing")
	}
	contractHash, err := domain.HashContract(cycle.Candidate.Contract)
	if err != nil {
		return fmt.Errorf("review contract: %w", err)
	}
	if contractHash != cycle.ContractHash {
		return fmt.Errorf("review contract hash does not match canonical contract_hash")
	}
	if err := s.validateCandidateProjection(cycle); err != nil {
		return err
	}
	if cycle.Approval == nil {
		return fmt.Errorf("review approval is missing")
	}
	if err := domain.ValidateApproval(*cycle.Approval, cycle.ID, cycle.Candidate.Contract); err != nil {
		return fmt.Errorf("review approval: %w", err)
	}
	var approvalArtifact *domain.Artifact
	for i := range cycle.Artifacts {
		if cycle.Artifacts[i].Kind == "approval" {
			if approvalArtifact != nil {
				return fmt.Errorf("review approval artifact is ambiguous")
			}
			approvalArtifact = &cycle.Artifacts[i]
		}
	}
	if approvalArtifact == nil {
		return fmt.Errorf("review approval artifact is missing")
	}
	currentApproval, err := s.Store.HashExisting("approval", approvalArtifact.Path)
	if err != nil {
		return fmt.Errorf("review approval artifact: %w", err)
	}
	if currentApproval.Digest != approvalArtifact.Digest {
		return fmt.Errorf("review approval artifact digest changed after capture")
	}
	var recordedApproval domain.Approval
	if err := s.Store.ReadJSON(cycle.ID, filepath.Base(approvalArtifact.Path), &recordedApproval); err != nil {
		return fmt.Errorf("read review approval artifact: %w", err)
	}
	if !reflect.DeepEqual(recordedApproval, *cycle.Approval) {
		return fmt.Errorf("canonical approval does not match approval artifact")
	}

	var measurementArtifact *domain.Artifact
	for i := range cycle.Artifacts {
		if cycle.Artifacts[i].Kind == "measurement" {
			if measurementArtifact != nil {
				return fmt.Errorf("review measurement artifact is ambiguous")
			}
			measurementArtifact = &cycle.Artifacts[i]
		}
	}
	if measurementArtifact == nil {
		return fmt.Errorf("review measurement artifact is missing")
	}
	current, err := s.Store.HashExisting("measurement", measurementArtifact.Path)
	if err != nil {
		return fmt.Errorf("review measurement artifact: %w", err)
	}
	if current.Digest != measurementArtifact.Digest {
		return fmt.Errorf("review measurement artifact digest changed after capture")
	}
	data, err := os.ReadFile(measurementArtifact.Path)
	if err != nil {
		return fmt.Errorf("read review measurement artifact: %w", err)
	}
	var recorded domain.Measurement
	if err := json.Unmarshal(data, &recorded); err != nil {
		return fmt.Errorf("decode review measurement artifact: %w", err)
	}
	if recorded != *cycle.Measurement {
		return fmt.Errorf("canonical measurement does not match measurement artifact")
	}
	return nil
}

func (s *Service) validateCandidateProjection(cycle domain.Cycle) error {
	if cycle.Judgment == nil || cycle.Candidate == nil {
		return fmt.Errorf("review candidate or judgment is missing")
	}
	if err := domain.ValidateJudgment(*cycle.Judgment); err != nil {
		return fmt.Errorf("review judgment: %w", err)
	}
	if cycle.Judgment.SelectedIndex == nil || *cycle.Judgment.SelectedIndex < 0 || *cycle.Judgment.SelectedIndex >= len(cycle.Judgment.Opportunities) {
		return fmt.Errorf("review judgment has no valid selected candidate")
	}
	var judgmentArtifact *domain.Artifact
	for i := range cycle.Artifacts {
		if cycle.Artifacts[i].Kind == "judgment" {
			if judgmentArtifact != nil {
				return fmt.Errorf("review judgment artifact is ambiguous")
			}
			judgmentArtifact = &cycle.Artifacts[i]
		}
	}
	if judgmentArtifact == nil {
		return fmt.Errorf("review judgment artifact is missing")
	}
	current, err := s.Store.HashExisting("judgment", judgmentArtifact.Path)
	if err != nil {
		return fmt.Errorf("review judgment artifact: %w", err)
	}
	if current.Digest != judgmentArtifact.Digest {
		return fmt.Errorf("review judgment artifact digest changed after capture")
	}
	var recorded domain.Judgment
	if err := s.Store.ReadJSON(cycle.ID, filepath.Base(judgmentArtifact.Path), &recorded); err != nil {
		return fmt.Errorf("read review judgment artifact: %w", err)
	}
	if !reflect.DeepEqual(recorded, *cycle.Judgment) {
		return fmt.Errorf("canonical judgment does not match judgment artifact")
	}
	selected := cycle.Judgment.Opportunities[*cycle.Judgment.SelectedIndex]
	if !reflect.DeepEqual(selected, *cycle.Candidate) {
		return fmt.Errorf("canonical candidate does not match selected judgment opportunity")
	}
	if fingerprint := domain.FingerprintCandidate(*cycle.Candidate); fingerprint != cycle.CandidateHash {
		return fmt.Errorf("canonical candidate fingerprint does not match candidate_hash")
	}
	return nil
}

func (s *Service) validateCompoundingEvidence(cycle domain.Cycle) error {
	if err := s.validateReviewProjection(cycle); err != nil {
		return fmt.Errorf("compound evidence: %w", err)
	}
	if err := harness.ValidateReview(*cycle.Review, cycle.ContractHash); err != nil {
		return fmt.Errorf("compound review: %w", err)
	}
	if err := s.validateReviewEvidence(*cycle.Review, cycle.Artifacts); err != nil {
		return fmt.Errorf("compound review evidence: %w", err)
	}
	if _, err := s.validatedReviewArtifact(cycle); err != nil {
		return err
	}
	if cycle.Resolution.ReviewerVerdict != cycle.Review.Verdict {
		return fmt.Errorf("compound resolution reviewer verdict does not match canonical review")
	}
	expected := cycle.Review.Verdict
	if expected == domain.VerdictPromote {
		if err := domain.ValidatePromotion(cycle.Candidate.Contract, *cycle.Measurement, *cycle.Review); err != nil {
			expected = domain.VerdictCloseFailure
		}
	}
	if cycle.Resolution.FinalVerdict != expected {
		return fmt.Errorf("compound resolution final verdict %s does not match deterministic verdict %s", cycle.Resolution.FinalVerdict, expected)
	}
	return nil
}

func (s *Service) validatedReviewArtifact(cycle domain.Cycle) (domain.Artifact, error) {
	var reviewArtifact *domain.Artifact
	for i := range cycle.Artifacts {
		if cycle.Artifacts[i].Kind == "review" {
			if reviewArtifact != nil {
				return domain.Artifact{}, fmt.Errorf("compound review artifact is ambiguous")
			}
			reviewArtifact = &cycle.Artifacts[i]
		}
	}
	if reviewArtifact == nil {
		return domain.Artifact{}, fmt.Errorf("compound review artifact is missing")
	}
	current, err := s.Store.HashExisting("review", reviewArtifact.Path)
	if err != nil {
		return domain.Artifact{}, fmt.Errorf("compound review artifact: %w", err)
	}
	if current.Digest != reviewArtifact.Digest {
		return domain.Artifact{}, fmt.Errorf("compound review artifact digest changed after capture")
	}
	var recorded domain.Review
	if err := s.Store.ReadJSON(cycle.ID, filepath.Base(reviewArtifact.Path), &recorded); err != nil {
		return domain.Artifact{}, fmt.Errorf("read compound review artifact: %w", err)
	}
	if !reflect.DeepEqual(recorded, *cycle.Review) {
		return domain.Artifact{}, fmt.Errorf("canonical review does not match review artifact")
	}
	return *reviewArtifact, nil
}

func (s *Service) validateRoadmapArtifact(cycle domain.Cycle) error {
	if !canonicalSHA256(cycle.RoadmapDigest) {
		return fmt.Errorf("canonical roadmap digest is not a lowercase SHA-256 digest")
	}
	var roadmapArtifact *domain.Artifact
	for i := range cycle.Artifacts {
		if cycle.Artifacts[i].Kind == "roadmap-output" {
			if roadmapArtifact != nil {
				return fmt.Errorf("roadmap artifact is ambiguous")
			}
			roadmapArtifact = &cycle.Artifacts[i]
		}
	}
	if roadmapArtifact == nil {
		return fmt.Errorf("roadmap artifact is missing")
	}
	expectedPath, err := s.Store.Path(cycle.ID, "roadmap-output.json")
	if err != nil {
		return err
	}
	if roadmapArtifact.Path != expectedPath {
		return fmt.Errorf("roadmap artifact is not the cycle-local snapshot")
	}
	if roadmapArtifact.Digest != cycle.RoadmapDigest {
		return fmt.Errorf("roadmap artifact digest does not match canonical roadmap digest")
	}
	current, err := s.Store.HashExisting("roadmap-output", roadmapArtifact.Path)
	if err != nil {
		return fmt.Errorf("rehash roadmap artifact: %w", err)
	}
	if current.Digest != roadmapArtifact.Digest {
		return fmt.Errorf("roadmap artifact changed after regeneration")
	}
	return nil
}

func (s *Service) validateReviewEvidence(review domain.Review, artifacts []domain.Artifact) error {
	validatedKinds := map[string]bool{}
	for _, ref := range review.Evidence {
		var matched *domain.Artifact
		for _, artifact := range artifacts {
			if artifact.Kind == ref.Kind && artifact.Digest == ref.Digest && (ref.ID == artifact.Path || ref.ID == filepath.Base(artifact.Path)) {
				artifactCopy := artifact
				matched = &artifactCopy
				break
			}
		}
		if matched == nil {
			return fmt.Errorf("review evidence %s:%s does not match a cycle artifact", ref.Kind, ref.ID)
		}
		current, err := s.Store.HashExisting(matched.Kind, matched.Path)
		if err != nil {
			return fmt.Errorf("hash review evidence %s:%s: %w", ref.Kind, ref.ID, err)
		}
		if current.Digest != matched.Digest {
			return fmt.Errorf("review evidence %s:%s changed after capture", ref.Kind, ref.ID)
		}
		validatedKinds[ref.Kind] = true
	}
	if review.Verdict == domain.VerdictPromote {
		for _, kind := range []string{"patch", "execution-report", "executor-transcript", "measurement"} {
			if !validatedKinds[kind] {
				return fmt.Errorf("promotion review is missing required %s evidence", kind)
			}
		}
	}
	return nil
}

func (s *Service) compoundBacklog(ctx context.Context, cycle *domain.Cycle) error {
	items, err := s.Backlog.List(ctx)
	if err != nil {
		return fmt.Errorf("read backlog for compounding: %w", err)
	}
	if cycle.Resolution.FinalVerdict == domain.VerdictPromote && cycle.IdempotencyKeys["backlog:promotion"] != "completed" {
		if existing, ok := adapters.FindCyclePromotion(items, cycle.ID); ok {
			cycle.PromotionBeadID = existing.ID
		} else {
			evidence, evidenceErr := s.promotionEvidence(*cycle)
			if evidenceErr != nil {
				return evidenceErr
			}
			id, createErr := s.Backlog.CreatePromotion(ctx, cycle.ID, cycle.ExperimentBeadID, *cycle.Candidate, 2, evidence)
			if createErr != nil {
				return fmt.Errorf("create promotion bead: %w", createErr)
			}
			cycle.PromotionBeadID = id
		}
		cycle.IdempotencyKeys["backlog:promotion"] = "completed"
		if err := s.persist(ctx, cycle); err != nil {
			return err
		}
	}
	if cycle.IdempotencyKeys["backlog:close"] == "completed" {
		return nil
	}
	if experiment, ok := adapters.FindBead(items, cycle.ExperimentBeadID); ok && strings.EqualFold(experiment.Status, "closed") {
		cycle.IdempotencyKeys["backlog:close"] = "completed"
		return s.persist(ctx, cycle)
	}
	reason := compoundReason(*cycle)
	if err := s.Backlog.Close(ctx, cycle.ExperimentBeadID, reason); err != nil {
		return fmt.Errorf("close experiment bead: %w", err)
	}
	cycle.IdempotencyKeys["backlog:close"] = "completed"
	return s.persist(ctx, cycle)
}

func (s *Service) compoundOutcome(ctx context.Context, cycle *domain.Cycle) error {
	if cycle.IdempotencyKeys["outcome:state"] == "completed" {
		return nil
	}
	outcome := outcomeFromCycle(*cycle)
	artifact, err := s.Store.WriteJSON(cycle.ID, "outcome", "outcome.json", outcome)
	if err != nil {
		return err
	}
	appendArtifact(cycle, artifact)
	if err := s.Kernel.SetOutcome(ctx, outcome); err != nil {
		return fmt.Errorf("record canonical outcome: %w", err)
	}
	cycle.IdempotencyKeys["outcome:state"] = "completed"
	return s.persist(ctx, cycle)
}

func (s *Service) compoundFeedback(ctx context.Context, cycle *domain.Cycle) error {
	signal := feedbackSignal(cycle.Resolution.FinalVerdict)
	outcomePath, err := s.Store.Path(cycle.ID, "outcome.json")
	if err != nil {
		return err
	}
	for _, ref := range cycle.Candidate.Evidence {
		if ref.Kind != "discovery" {
			continue
		}
		key := "feedback:" + ref.ID
		if cycle.IdempotencyKeys[key] == "completed" {
			continue
		}
		if cycle.IdempotencyKeys[key] != "started" {
			cycle.IdempotencyKeys[key] = "started"
			if err := s.persist(ctx, cycle); err != nil {
				return err
			}
		}
		idempotencyKey := "remontoire:" + cycle.ID + ":" + ref.ID
		if err := s.Kernel.RecordDiscoveryFeedback(ctx, ref.ID, signal, outcomePath, idempotencyKey); err != nil {
			return fmt.Errorf("record discovery feedback %s: %w", ref.ID, err)
		}
		cycle.IdempotencyKeys[key] = "completed"
		if err := s.persist(ctx, cycle); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) finalizeReceipt(ctx context.Context, cycle *domain.Cycle) error {
	if cycle.SignedReceiptID != "" && cycle.IdempotencyKeys["receipt:attempt"] == "completed" {
		return nil
	}
	attempt := cycle.IdempotencyKeys["receipt:attempt"]
	if attempt == "started" || attempt == "completed" {
		receiptArtifact, err := s.receiptArtifact(*cycle)
		if err != nil {
			return err
		}
		var signature receiptSignature
		if err := s.Store.ReadJSON(cycle.ID, "receipt-signature.json", &signature); err == nil {
			return s.acceptReceipt(ctx, cycle, receiptArtifact, signature, false)
		} else if !os.IsNotExist(err) {
			return err
		}

		if attempt == "completed" {
			receiptID, findErr := s.Kernel.FindReceipt(ctx, cycle.RunID, receiptArtifact.Digest)
			if findErr != nil {
				return fmt.Errorf("%w: completed receipt has no recoverable signature: %v", ErrReceiptIndeterminate, findErr)
			}
			return s.acceptReceipt(ctx, cycle, receiptArtifact, receiptSignature{ReceiptID: receiptID, ContentHash: receiptArtifact.Digest}, true)
		}
		return s.emitOrRecoverReceipt(ctx, cycle, receiptArtifact)
	}

	terminalAt := s.now()
	value, err := receiptpkg.Build(*cycle, terminalAt)
	if err != nil {
		return err
	}
	data, digest, err := receiptpkg.Marshal(value)
	if err != nil {
		return err
	}
	artifact, err := s.Store.WriteBytes(cycle.ID, "receipt", "receipt.json", data)
	if err != nil {
		return err
	}
	if artifact.Digest != digest {
		return fmt.Errorf("receipt content digest mismatch")
	}
	appendArtifact(cycle, artifact)
	cycle.IdempotencyKeys["receipt:content"] = digest
	cycle.IdempotencyKeys["receipt:attempt"] = "started"
	if err := s.persist(ctx, cycle); err != nil {
		return err
	}
	return s.emitOrRecoverReceipt(ctx, cycle, artifact)
}

func (s *Service) receiptArtifact(cycle domain.Cycle) (domain.Artifact, error) {
	receiptPath, err := s.Store.Path(cycle.ID, "receipt.json")
	if err != nil {
		return domain.Artifact{}, err
	}
	artifact, err := s.Store.HashExisting("receipt", receiptPath)
	if err != nil {
		return domain.Artifact{}, fmt.Errorf("%w: hash local receipt: %v", ErrReceiptIndeterminate, err)
	}
	if expected := cycle.IdempotencyKeys["receipt:content"]; expected == "" || expected != artifact.Digest {
		return domain.Artifact{}, fmt.Errorf("%w: canonical content hash does not match receipt", ErrReceiptIndeterminate)
	}
	return artifact, nil
}

func (s *Service) emitOrRecoverReceipt(ctx context.Context, cycle *domain.Cycle, receiptArtifact domain.Artifact) error {
	digest := receiptArtifact.Digest
	if receiptID, err := s.Kernel.FindReceipt(ctx, cycle.RunID, digest); err == nil {
		return s.acceptReceipt(ctx, cycle, receiptArtifact, receiptSignature{ReceiptID: receiptID, ContentHash: digest}, true)
	} else if !errors.Is(err, adapters.ErrNotFound) {
		return fmt.Errorf("%w: resolve existing receipt: %v", ErrReceiptIndeterminate, err)
	}

	model := "remontoire"
	if cycle.Resolution != nil {
		if cycle.Resolution.ReviewerModel != "" {
			model = cycle.Resolution.ReviewerModel
		} else if cycle.Resolution.ReviewerBackend != "" {
			model = cycle.Resolution.ReviewerBackend
		}
	}
	receiptID, emitErr := s.Kernel.EmitReceipt(ctx, cycle.RunID, model, digest)
	if emitErr != nil {
		resolvedID, findErr := s.Kernel.FindReceipt(ctx, cycle.RunID, digest)
		if findErr == nil {
			return s.acceptReceipt(ctx, cycle, receiptArtifact, receiptSignature{ReceiptID: resolvedID, ContentHash: digest}, true)
		}
		if !errors.Is(findErr, adapters.ErrNotFound) {
			return fmt.Errorf("%w: emit failed (%v) and lookup failed: %v", ErrReceiptIndeterminate, emitErr, findErr)
		}
		return fmt.Errorf("emit signed receipt: %w", emitErr)
	}
	return s.acceptReceipt(ctx, cycle, receiptArtifact, receiptSignature{ReceiptID: receiptID, ContentHash: digest}, false)
}

func (s *Service) acceptReceipt(ctx context.Context, cycle *domain.Cycle, receiptArtifact domain.Artifact, signature receiptSignature, exactBound bool) error {
	if signature.ReceiptID == "" || signature.ContentHash == "" || signature.ContentHash != receiptArtifact.Digest {
		return fmt.Errorf("%w: local content hash does not match receipt", ErrReceiptIndeterminate)
	}
	if cycle.SignedReceiptID != "" && cycle.SignedReceiptID != signature.ReceiptID {
		return fmt.Errorf("%w: canonical receipt id does not match local signature", ErrReceiptIndeterminate)
	}
	if !exactBound {
		exactID, err := s.Kernel.FindReceipt(ctx, cycle.RunID, receiptArtifact.Digest)
		if err != nil {
			return fmt.Errorf("%w: resolve exact receipt: %v", ErrReceiptIndeterminate, err)
		}
		if exactID != signature.ReceiptID {
			return fmt.Errorf("%w: local signature does not match exact receipt %s", ErrReceiptIndeterminate, exactID)
		}
	}
	if err := s.Kernel.VerifyReceipt(ctx, signature.ReceiptID); err != nil {
		return fmt.Errorf("%w: verify %s: %v", ErrReceiptIndeterminate, signature.ReceiptID, err)
	}
	appendArtifact(cycle, receiptArtifact)
	signatureArtifact, err := s.Store.WriteJSON(cycle.ID, "receipt-signature", "receipt-signature.json", signature)
	if err != nil {
		return err
	}
	appendArtifact(cycle, signatureArtifact)
	cycle.SignedReceiptID = signature.ReceiptID
	cycle.IdempotencyKeys["receipt:content"] = signature.ContentHash
	cycle.IdempotencyKeys["receipt:attempt"] = "completed"
	return s.persist(ctx, cycle)
}

func (s *Service) advanceOnce(ctx context.Context, cycle *domain.Cycle, key, predecessorPhase, targetPhase string) error {
	if cycle.IdempotencyKeys[key] == "completed" {
		return nil
	}
	phase, err := s.Kernel.RunPhase(ctx, cycle.RunID)
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", ErrAdvanceIndeterminate, key, err)
	}
	if phase == targetPhase {
		cycle.IdempotencyKeys[key] = "completed"
		return s.persist(ctx, cycle)
	}
	if phase != predecessorPhase {
		return fmt.Errorf("%w: %s expected phase %s or %s, got %s", ErrAdvanceIndeterminate, key, predecessorPhase, targetPhase, phase)
	}
	if cycle.IdempotencyKeys[key] != "started" {
		cycle.IdempotencyKeys[key] = "started"
		if err := s.persist(ctx, cycle); err != nil {
			return err
		}
	}
	if err := s.Kernel.AdvanceRun(ctx, cycle.RunID); err != nil {
		phase, phaseErr := s.Kernel.RunPhase(ctx, cycle.RunID)
		if phaseErr != nil || phase != targetPhase {
			return fmt.Errorf("advance intercore run to %s: %w", targetPhase, err)
		}
	}
	phase, err = s.Kernel.RunPhase(ctx, cycle.RunID)
	if err != nil {
		return fmt.Errorf("%w: verify %s: %v", ErrAdvanceIndeterminate, key, err)
	}
	if phase != targetPhase {
		return fmt.Errorf("%w: %s advanced to unexpected phase %s", ErrAdvanceIndeterminate, key, phase)
	}
	cycle.IdempotencyKeys[key] = "completed"
	return s.persist(ctx, cycle)
}

func (s *Service) terminalizeRun(ctx context.Context, cycle *domain.Cycle) error {
	phases := []string{"observe", "rank", "propose", "execute", "review", "compound"}
	phase, status, err := s.Kernel.RunStatus(ctx, cycle.RunID)
	if err != nil {
		return fmt.Errorf("inspect terminal run state: %w", err)
	}
	if phase == "compound" && status == "completed" {
		if cycle.IdempotencyKeys["run:terminal"] != "completed" {
			cycle.IdempotencyKeys["run:terminal"] = "completed"
			return s.persist(ctx, cycle)
		}
		return nil
	}
	if status != "active" {
		return fmt.Errorf("run terminalization expected active or completed status, got %s at phase %s", status, phase)
	}
	index := -1
	for i, candidate := range phases {
		if candidate == phase {
			index = i
			break
		}
	}
	if index < 0 || index == len(phases)-1 {
		return fmt.Errorf("run terminalization cannot advance phase %s with status %s", phase, status)
	}
	for i := index + 1; i < len(phases); i++ {
		if err := s.advanceOnce(ctx, cycle, "run:terminal:"+phases[i], phases[i-1], phases[i]); err != nil {
			return fmt.Errorf("terminalize run at %s: %w", phases[i], err)
		}
	}
	phase, status, err = s.Kernel.RunStatus(ctx, cycle.RunID)
	if err != nil {
		return fmt.Errorf("verify terminal run state: %w", err)
	}
	if phase != "compound" || status != "completed" {
		return fmt.Errorf("run terminalization ended at %s/%s, want compound/completed", phase, status)
	}
	cycle.IdempotencyKeys["run:terminal"] = "completed"
	return s.persist(ctx, cycle)
}

func outcomeFromCycle(cycle domain.Cycle) domain.OutcomeSummary {
	rationale := cycle.Review.Rationale
	if cycle.Resolution.OverrideReason != "" {
		rationale = cycle.Resolution.OverrideReason + "; reviewer: " + rationale
	}
	return domain.OutcomeSummary{
		SchemaVersion: domain.OutcomeSchemaV1, CycleID: cycle.ID, Portfolio: cycle.Portfolio,
		Project: cycle.Candidate.Project, Title: cycle.Candidate.Title, CandidateHash: cycle.CandidateHash,
		ContractHash: cycle.ContractHash, ExperimentBeadID: cycle.ExperimentBeadID,
		PromotionBeadID: cycle.PromotionBeadID, FinalVerdict: cycle.Resolution.FinalVerdict,
		MetricName: cycle.Measurement.MetricName, MetricValue: cycle.Measurement.Value,
		MetricTarget: cycle.Candidate.Contract.Metric.Target, MetricDirection: cycle.Candidate.Contract.Metric.Direction,
		Rationale: rationale, RecordedAt: cycle.Resolution.ResolvedAt,
	}
}

func compoundReason(cycle domain.Cycle) string {
	return fmt.Sprintf("Remontoire %s: %s=%.6g (target %.6g, %s). %s",
		cycle.Resolution.FinalVerdict, cycle.Measurement.MetricName, cycle.Measurement.Value,
		cycle.Candidate.Contract.Metric.Target, cycle.Candidate.Contract.Metric.Direction,
		outcomeFromCycle(cycle).Rationale)
}

func (s *Service) promotionEvidence(cycle domain.Cycle) (string, error) {
	lines := []string{
		"Verdict: " + string(cycle.Resolution.FinalVerdict),
		fmt.Sprintf("Metric: %s=%.6g (target %.6g, %s)", cycle.Measurement.MetricName, cycle.Measurement.Value, cycle.Candidate.Contract.Metric.Target, cycle.Candidate.Contract.Metric.Direction),
		"Contract: " + cycle.ContractHash,
	}
	evidence := map[string]string{}
	for _, ref := range cycle.Review.Evidence {
		if existing := evidence[ref.Kind]; existing != "" && existing != ref.Digest {
			return "", fmt.Errorf("promotion evidence kind %s is ambiguous", ref.Kind)
		}
		evidence[ref.Kind] = ref.Digest
	}
	for _, kind := range []string{"patch", "execution-report", "executor-transcript", "measurement"} {
		if evidence[kind] == "" {
			return "", fmt.Errorf("promotion evidence is missing %s", kind)
		}
		lines = append(lines, kind+": "+evidence[kind])
	}
	reviewArtifact, err := s.validatedReviewArtifact(cycle)
	if err != nil {
		return "", err
	}
	lines = append(lines, "review: "+reviewArtifact.Digest)
	return strings.Join(lines, "\n"), nil
}

func feedbackSignal(verdict domain.Verdict) string {
	switch verdict {
	case domain.VerdictPromote:
		return "promote"
	case domain.VerdictCloseSuccess:
		return "boost"
	case domain.VerdictCloseFailure:
		return "penalize"
	default:
		return "adjust_priority"
	}
}

func appendArtifact(cycle *domain.Cycle, artifact domain.Artifact) {
	for _, existing := range cycle.Artifacts {
		if existing.Kind == artifact.Kind && existing.Path == artifact.Path && existing.Digest == artifact.Digest {
			return
		}
	}
	cycle.Artifacts = append(cycle.Artifacts, artifact)
}

func canonicalSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
