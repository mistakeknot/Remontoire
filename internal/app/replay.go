package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	cyclepkg "github.com/mistakeknot/Remontoire/internal/cycle"
	"github.com/mistakeknot/Remontoire/internal/domain"
	receiptpkg "github.com/mistakeknot/Remontoire/internal/receipt"
	"github.com/mistakeknot/Remontoire/internal/strictjson"
)

const ReplaySchemaV1 = "remontoire.replay/v1"

const ReplayVerificationStoredContent = "stored-content-self-consistency"

type ReplayResult struct {
	SchemaVersion     string `json:"schema_version"`
	CycleID           string `json:"cycle_id"`
	Verified          bool   `json:"verified"`
	VerificationScope string `json:"verification_scope"`
	SignatureVerified bool   `json:"signature_verified"`
	DecisionHash      string `json:"decision_hash"`
	ContractHash      string `json:"contract_hash,omitempty"`
	ReceiptDigest     string `json:"receipt_digest"`
	InputsVerified    int    `json:"inputs_verified"`
	OutputsVerified   int    `json:"outputs_verified"`
}

func (a *Application) ShowReceipt(cycleID string) (domain.Receipt, error) {
	var value domain.Receipt
	if err := a.Store.ReadJSON(cycleID, "receipt.json", &value); err != nil {
		return domain.Receipt{}, fmt.Errorf("show receipt: %w", err)
	}
	if value.SchemaVersion != domain.ReceiptSchemaV1 || value.Cycle.ID != cycleID {
		return domain.Receipt{}, fmt.Errorf("show receipt: receipt identity or schema is invalid")
	}
	return value, nil
}

func (a *Application) ReplayReceipt(cycleID string) (ReplayResult, error) {
	value, err := a.ShowReceipt(cycleID)
	if err != nil {
		return ReplayResult{}, err
	}
	cycleDir, err := a.Store.CycleDir(cycleID)
	if err != nil {
		return ReplayResult{}, err
	}
	cycleRoot, err := os.OpenRoot(cycleDir)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("open replay cycle directory: %w", err)
	}
	defer cycleRoot.Close()
	all := append(append([]domain.Artifact(nil), value.InputArtifacts...), value.OutputArtifacts...)
	seen := map[string]bool{}
	dataByArtifact := map[string][]byte{}
	for _, artifact := range all {
		key := artifact.Kind + "\x00" + artifact.Path
		if seen[key] {
			return ReplayResult{}, fmt.Errorf("replay artifact %s is duplicated", artifact.Kind)
		}
		seen[key] = true
		if !replayArtifactPathAllowed(cycleDir, artifact) {
			return ReplayResult{}, fmt.Errorf("replay artifact %s has an unsafe path", artifact.Kind)
		}
		data, readErr := readReplayArtifact(cycleRoot, cycleDir, artifact)
		if readErr != nil {
			return ReplayResult{}, readErr
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != artifact.Digest {
			return ReplayResult{}, fmt.Errorf("replay artifact %s digest changed", artifact.Kind)
		}
		dataByArtifact[key] = data
	}
	rebuilt, err := receiptpkg.Build(value.Cycle, value.TerminalAt)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("rebuild terminal decision: %w", err)
	}
	if !reflect.DeepEqual(rebuilt.InputArtifacts, value.InputArtifacts) || !reflect.DeepEqual(rebuilt.OutputArtifacts, value.OutputArtifacts) {
		return ReplayResult{}, fmt.Errorf("receipt artifact partition does not match the terminal cycle")
	}
	if value.Cycle.Stage != domain.StageCompleted && value.Cycle.Stage != domain.StageFailed {
		return ReplayResult{}, fmt.Errorf("replay receipt cycle is not terminal")
	}
	roadmapPath, err := a.Store.Path(cycleID, "roadmap-output.json")
	if err != nil {
		return ReplayResult{}, err
	}
	if err := validateReplayRoadmap(value.Cycle, value.OutputArtifacts, roadmapPath); err != nil {
		return ReplayResult{}, err
	}

	observationArtifact, hasObservation, err := optionalReplayArtifact(value.InputArtifacts, "observation")
	if err != nil {
		return ReplayResult{}, err
	}
	judgmentArtifact, hasJudgment, err := optionalReplayArtifact(value.OutputArtifacts, "judgment")
	if err != nil {
		return ReplayResult{}, err
	}
	if value.Cycle.Stage == domain.StageCompleted && (!hasObservation || !hasJudgment) {
		return ReplayResult{}, fmt.Errorf("completed replay receipt requires observation and judgment artifacts")
	}
	if hasJudgment && !hasObservation {
		return ReplayResult{}, fmt.Errorf("replay judgment artifact requires an observation artifact")
	}
	if !hasObservation && value.Cycle.IdempotencyKeys["observation:capture"] == "completed" {
		return ReplayResult{}, fmt.Errorf("replay receipt marks observation complete without its artifact")
	}

	var observation *cyclepkg.Observation
	if hasObservation {
		var decoded cyclepkg.Observation
		if err := decodeReplayJSON(dataByArtifact[artifactKey(observationArtifact)], &decoded); err != nil {
			return ReplayResult{}, fmt.Errorf("decode replay observation: %w", err)
		}
		inputs := make([]cyclepkg.ReplayInput, 0, len(value.InputArtifacts))
		for _, artifact := range value.InputArtifacts {
			switch artifact.Kind {
			case "beads", "discoveries", "interest-profile", "ockham", "outcomes", "roadmap", "observation":
				inputs = append(inputs, cyclepkg.ReplayInput{Artifact: artifact, Data: dataByArtifact[artifactKey(artifact)]})
			}
		}
		if err := cyclepkg.ValidateReplayObservation(value.Cycle, decoded, inputs); err != nil {
			return ReplayResult{}, err
		}
		observation = &decoded
	}
	var judgment *domain.Judgment
	if hasJudgment {
		var decoded domain.Judgment
		if err := decodeReplayJSON(dataByArtifact[artifactKey(judgmentArtifact)], &decoded); err != nil {
			return ReplayResult{}, fmt.Errorf("decode replay judgment: %w", err)
		}
		if err := cyclepkg.ValidateReplayJudgment(decoded, *observation); err != nil {
			return ReplayResult{}, fmt.Errorf("validate replay judgment: %w", err)
		}
		judgment = &decoded
	}
	proposalArtifact, hasProposalBacklog, err := optionalReplayArtifact(value.InputArtifacts, "proposal-backlog")
	if err != nil {
		return ReplayResult{}, err
	}
	var proposalBacklog []adapters.Bead
	if hasProposalBacklog {
		if err := decodeReplayJSON(dataByArtifact[artifactKey(proposalArtifact)], &proposalBacklog); err != nil {
			return ReplayResult{}, fmt.Errorf("decode replay proposal backlog: %w", err)
		}
	}
	contractHash, err := replayDecision(value, judgment, proposalBacklog, hasProposalBacklog)
	if err != nil {
		return ReplayResult{}, err
	}
	if rebuilt.DecisionHash != value.DecisionHash {
		return ReplayResult{}, fmt.Errorf("replayed decision hash %s does not match receipt %s", rebuilt.DecisionHash, value.DecisionHash)
	}
	canonical, canonicalDigest, err := receiptpkg.Marshal(value)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("marshal replayed receipt: %w", err)
	}
	receiptPath, err := a.Store.Path(cycleID, "receipt.json")
	if err != nil {
		return ReplayResult{}, err
	}
	stored, err := os.ReadFile(receiptPath)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("read stored receipt: %w", err)
	}
	if !bytes.Equal(stored, canonical) {
		return ReplayResult{}, fmt.Errorf("stored receipt bytes are not canonical")
	}
	sum := sha256.Sum256(stored)
	storedDigest := hex.EncodeToString(sum[:])
	if storedDigest != canonicalDigest {
		return ReplayResult{}, fmt.Errorf("stored receipt digest does not match canonical receipt")
	}
	return ReplayResult{
		SchemaVersion: ReplaySchemaV1, CycleID: cycleID, Verified: true,
		VerificationScope: ReplayVerificationStoredContent, SignatureVerified: false,
		DecisionHash: value.DecisionHash, ContractHash: contractHash, ReceiptDigest: storedDigest,
		InputsVerified: len(value.InputArtifacts), OutputsVerified: len(value.OutputArtifacts),
	}, nil
}

func replayDecision(value domain.Receipt, judgment *domain.Judgment, proposalBacklog []adapters.Bead, hasProposalBacklog bool) (string, error) {
	if judgment == nil {
		if value.Cycle.Judgment != nil || value.Cycle.Candidate != nil || value.Cycle.CandidateHash != "" || value.Cycle.ContractHash != "" || value.Cycle.NoOpReason != "" || hasProposalBacklog {
			return "", fmt.Errorf("replay receipt decision fields require a judgment artifact")
		}
		return "", nil
	}
	if value.Cycle.Judgment == nil || !reflect.DeepEqual(*judgment, *value.Cycle.Judgment) {
		return "", fmt.Errorf("replay judgment artifact does not match receipt judgment")
	}
	if value.Cycle.Candidate == nil {
		if hasProposalBacklog {
			return "", fmt.Errorf("replay proposal backlog exists without a selected candidate")
		}
		if value.Cycle.ContractHash != "" || value.Cycle.CandidateHash != "" {
			return "", fmt.Errorf("replay receipt has hashes without a candidate")
		}
		if value.Cycle.Stage == domain.StageFailed {
			if value.Cycle.NoOpReason != "" {
				return "", fmt.Errorf("failed replay receipt has a forged cycle no-op reason")
			}
			return "", nil
		}
		if judgment.SelectedIndex != nil {
			return "", fmt.Errorf("replay selected judgment has no receipt candidate")
		}
		if value.Cycle.NoOpReason != judgment.NoOpReason {
			return "", fmt.Errorf("replay no-op reason does not match the judgment")
		}
		return "", nil
	}
	if judgment.SelectedIndex == nil || !reflect.DeepEqual(judgment.Opportunities[*judgment.SelectedIndex], *value.Cycle.Candidate) {
		return "", fmt.Errorf("replay selected judgment does not match receipt candidate")
	}
	contractHash, err := domain.HashContract(value.Cycle.Candidate.Contract)
	if err != nil {
		return "", fmt.Errorf("replay contract: %w", err)
	}
	if contractHash != value.Cycle.ContractHash {
		return "", fmt.Errorf("replay contract hash %s does not match receipt %s", contractHash, value.Cycle.ContractHash)
	}
	if domain.FingerprintCandidate(*value.Cycle.Candidate) != value.Cycle.CandidateHash {
		return "", fmt.Errorf("replay candidate fingerprint does not match receipt")
	}
	if value.Cycle.Stage == domain.StageFailed {
		if value.Cycle.NoOpReason != "" || hasProposalBacklog {
			return "", fmt.Errorf("failed replay receipt has an unresolved selected-candidate no-op")
		}
		return contractHash, nil
	}
	switch value.Cycle.Mode {
	case domain.ModeShadow:
		if value.Cycle.NoOpReason != cyclepkg.NoOpReasonShadowMode || hasProposalBacklog {
			return "", fmt.Errorf("replay shadow no-op resolution is invalid")
		}
	case domain.ModeProposal:
		switch value.Cycle.NoOpReason {
		case "":
			if hasProposalBacklog {
				return "", fmt.Errorf("replay proposal backlog exists without duplicate resolution")
			}
		case cyclepkg.NoOpReasonDuplicateFingerprint:
			if !hasProposalBacklog || !adapters.HasFingerprint(proposalBacklog, value.Cycle.CandidateHash) {
				return "", fmt.Errorf("replay duplicate no-op is not bound to the proposal backlog")
			}
		default:
			return "", fmt.Errorf("replay selected-candidate no-op reason is invalid")
		}
	default:
		return "", fmt.Errorf("replay cycle mode is invalid")
	}
	return contractHash, nil
}

func optionalReplayArtifact(artifacts []domain.Artifact, kind string) (domain.Artifact, bool, error) {
	var found domain.Artifact
	for _, artifact := range artifacts {
		if artifact.Kind != kind {
			continue
		}
		if found.Kind != "" {
			return domain.Artifact{}, false, fmt.Errorf("replay %s artifact is ambiguous", kind)
		}
		found = artifact
	}
	return found, found.Kind != "", nil
}

func validateReplayRoadmap(cycle domain.Cycle, outputs []domain.Artifact, expectedPath string) error {
	artifact, present, err := optionalReplayArtifact(outputs, "roadmap-output")
	if err != nil {
		return err
	}
	if cycle.RoadmapDigest == "" {
		if present {
			return fmt.Errorf("replay roadmap output exists without a canonical digest")
		}
		return nil
	}
	if len(cycle.RoadmapDigest) != 64 || cycle.RoadmapDigest != strings.ToLower(cycle.RoadmapDigest) {
		return fmt.Errorf("replay roadmap digest is not canonical SHA-256")
	}
	if _, err := hex.DecodeString(cycle.RoadmapDigest); err != nil {
		return fmt.Errorf("replay roadmap digest is not canonical SHA-256")
	}
	if !present {
		return fmt.Errorf("replay roadmap output is missing")
	}
	if artifact.Path != expectedPath || artifact.Digest != cycle.RoadmapDigest {
		return fmt.Errorf("replay roadmap output does not match the canonical digest")
	}
	return nil
}

func artifactKey(artifact domain.Artifact) string {
	return artifact.Kind + "\x00" + artifact.Path
}

func decodeReplayJSON(data []byte, target any) error {
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func replayArtifactPathAllowed(cycleDir string, artifact domain.Artifact) bool {
	cleaned := filepath.Clean(artifact.Path)
	if cleaned != artifact.Path || !filepath.IsAbs(cleaned) {
		return false
	}
	rel, err := filepath.Rel(cycleDir, cleaned)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return true
	}
	return false
}

func readReplayArtifact(root *os.Root, cycleDir string, artifact domain.Artifact) ([]byte, error) {
	pathInfo, err := os.Lstat(artifact.Path)
	if err != nil {
		return nil, fmt.Errorf("replay artifact %s: %w", artifact.Kind, err)
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("replay artifact %s is not a regular file", artifact.Kind)
	}
	rel, err := filepath.Rel(cycleDir, artifact.Path)
	if err != nil {
		return nil, fmt.Errorf("replay artifact %s: %w", artifact.Kind, err)
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open replay artifact %s: %w", artifact.Kind, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat replay artifact %s: %w", artifact.Kind, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return nil, fmt.Errorf("replay artifact %s changed during verification", artifact.Kind)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read replay artifact %s: %w", artifact.Kind, err)
	}
	return data, nil
}
