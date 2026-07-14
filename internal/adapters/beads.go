package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

type Bead struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria string   `json:"acceptance_criteria"`
	Status             string   `json:"status"`
	Priority           int      `json:"priority"`
	IssueType          string   `json:"issue_type"`
	DependentCount     int      `json:"dependent_count"`
	Labels             []string `json:"labels"`
	Dependencies       []struct {
		DependsOnID string `json:"depends_on_id"`
		Type        string `json:"type"`
	} `json:"dependencies"`
}

type Beads struct {
	Binary string
	Dir    string
	Runner Runner
}

func (b Beads) List(ctx context.Context) ([]Bead, error) {
	result, err := b.run(ctx, "list", "--all", "--limit=0", "--json")
	if err != nil {
		return nil, err
	}
	var beads []Bead
	if err := json.Unmarshal(result.Stdout, &beads); err != nil {
		return nil, fmt.Errorf("decode beads list: %w", err)
	}
	return beads, nil
}

func (b Beads) ReadyPromotions(ctx context.Context) ([]Bead, error) {
	result, err := b.run(ctx, "--sandbox", "ready", "--label=remontoire-promotion", "--limit=0", "--json")
	if err != nil {
		return nil, err
	}
	var beads []Bead
	if err := json.Unmarshal(result.Stdout, &beads); err != nil {
		return nil, fmt.Errorf("decode ready promotion beads: %w", err)
	}
	return beads, nil
}

func HasFingerprint(beads []Bead, fingerprint string) bool {
	want := "remontoire:fingerprint:" + fingerprint
	for _, bead := range beads {
		for _, label := range bead.Labels {
			if label == want {
				return true
			}
		}
	}
	return false
}

func FindCycleExperiment(beads []Bead, cycleID string) (Bead, bool) {
	want := "remontoire:cycle:" + cycleID
	for _, bead := range beads {
		if hasLabel(bead, "remontoire-experiment") && hasLabel(bead, want) {
			return bead, true
		}
	}
	return Bead{}, false
}

func FindCyclePromotion(beads []Bead, cycleID string) (Bead, bool) {
	want := "remontoire:cycle:" + cycleID
	for _, bead := range beads {
		if hasLabel(bead, "remontoire-promotion") && hasLabel(bead, want) {
			return bead, true
		}
	}
	return Bead{}, false
}

func FindBead(beads []Bead, beadID string) (Bead, bool) {
	for _, bead := range beads {
		if bead.ID == beadID {
			return bead, true
		}
	}
	return Bead{}, false
}

func hasLabel(bead Bead, want string) bool {
	for _, label := range bead.Labels {
		if label == want {
			return true
		}
	}
	return false
}

func (b Beads) CreateExperiment(ctx context.Context, cycleID, fingerprint string, candidate domain.Candidate) (string, error) {
	if candidate.Priority != 4 {
		return "", fmt.Errorf("experiment candidate must be P4")
	}
	if err := domain.ValidateEvidenceContract(candidate.Contract); err != nil {
		return "", fmt.Errorf("experiment contract: %w", err)
	}
	contractHash, err := domain.HashContract(candidate.Contract)
	if err != nil {
		return "", err
	}
	sources := make([]string, 0, len(candidate.Evidence))
	for _, ref := range candidate.Evidence {
		sources = append(sources, ref.Kind+":"+ref.ID)
	}
	metadata, err := json.Marshal(map[string]any{
		"schema_version":   domain.ContractSchemaV1,
		"cycle_id":         cycleID,
		"fingerprint":      fingerprint,
		"contract_hash":    contractHash,
		"repository":       candidate.Contract.Repository,
		"allowed_paths":    candidate.Contract.AllowedPaths,
		"metric":           candidate.Contract.Metric,
		"benchmark":        candidate.Contract.Benchmark,
		"budget":           candidate.Contract.Budget,
		"executor":         candidate.Contract.Executor,
		"evidence_sources": sources,
	})
	if err != nil {
		return "", fmt.Errorf("marshal experiment metadata: %w", err)
	}
	description := fmt.Sprintf("%s\n\nHypothesis: %s\n\nFalsifier: %s\n\nContract hash: %s", candidate.Summary, candidate.Contract.Hypothesis, candidate.Contract.Falsifier, contractHash)
	labels := strings.Join([]string{
		"remontoire-experiment",
		"remontoire:cycle:" + cycleID,
		"remontoire:fingerprint:" + fingerprint,
	}, ",")
	result, err := b.run(ctx,
		"create", "--silent",
		"--title=[Experiment] "+candidate.Title,
		"--type=task",
		"--priority=P4",
		"--labels="+labels,
		"--description="+description,
		"--acceptance="+candidate.Contract.PromotionCriteria,
		"--external-ref=remontoire:"+cycleID,
		"--metadata="+string(metadata),
	)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(result.Stdout))
	if id == "" || strings.ContainsAny(id, " \t\r\n") {
		return "", fmt.Errorf("bd create returned invalid id %q", id)
	}
	return id, nil
}

func (b Beads) Close(ctx context.Context, beadID, reason string) error {
	_, err := b.run(ctx, "close", beadID, "--reason="+reason)
	return err
}

func (b Beads) AddNote(ctx context.Context, beadID, note string) error {
	_, err := b.run(ctx, "update", beadID, "--append-notes="+note)
	return err
}

func (b Beads) CreatePromotion(ctx context.Context, cycleID, experimentID string, candidate domain.Candidate, priority int, evidence string) (string, error) {
	if priority < 0 || priority > 4 {
		return "", fmt.Errorf("promotion priority %d is invalid", priority)
	}
	if strings.TrimSpace(evidence) == "" {
		return "", fmt.Errorf("promotion evidence is required")
	}
	result, err := b.run(ctx,
		"create", "--silent",
		"--title="+candidate.Title,
		"--type=feature",
		"--priority=P"+strconv.Itoa(priority),
		"--labels=remontoire-promotion,remontoire:cycle:"+cycleID,
		"--description=Promoted from bounded experiment "+experimentID+". "+candidate.Summary+"\n\nMeasured evidence:\n"+strings.TrimSpace(evidence),
		"--acceptance="+candidate.Contract.PromotionCriteria,
		"--deps=discovered-from:"+experimentID,
		"--external-ref=remontoire:"+cycleID,
	)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(result.Stdout))
	if id == "" {
		return "", fmt.Errorf("bd create promotion returned no id")
	}
	return id, nil
}

func (b Beads) run(ctx context.Context, args ...string) (Result, error) {
	if b.Runner == nil {
		return Result{}, fmt.Errorf("beads runner is required")
	}
	binary := b.Binary
	if binary == "" {
		binary = "bd"
	}
	result, err := b.Runner.Run(ctx, Invocation{Name: binary, Args: args, Dir: b.Dir})
	if err != nil {
		return result, fmt.Errorf("bd %s: %w", strings.Join(args[:min(2, len(args))], " "), err)
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("bd %s exited %d", strings.Join(args[:min(2, len(args))], " "), result.ExitCode)
	}
	return result, nil
}
