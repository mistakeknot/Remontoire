package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

var (
	ErrLockHeld = errors.New("portfolio cycle lock is held")
	ErrNotFound = errors.New("canonical state not found")
)

type Discovery struct {
	ID             string  `json:"id"`
	Source         string  `json:"source"`
	SourceID       string  `json:"source_id"`
	Title          string  `json:"title"`
	Summary        string  `json:"summary"`
	URL            string  `json:"url"`
	RawMetadata    string  `json:"raw_metadata"`
	RelevanceScore float64 `json:"relevance_score"`
	ConfidenceTier string  `json:"confidence_tier"`
	Status         string  `json:"status"`
	BeadID         *string `json:"bead_id"`
	DiscoveredAt   int64   `json:"discovered_at"`
}

type InterestProfile struct {
	KeywordWeights string `json:"keyword_weights"`
	SourceWeights  string `json:"source_weights"`
	UpdatedAt      int64  `json:"updated_at"`
}

type Intercore struct {
	Binary string
	Dir    string
	Runner Runner
}

func (c Intercore) Health(ctx context.Context) error {
	_, err := c.run(ctx, nil, "health")
	return err
}

func (c Intercore) AcquireCycleLock(ctx context.Context, portfolio, owner, timeout string) error {
	result, err := c.invoke(ctx, nil, "lock", "acquire", "remontoire-cycle", portfolio, "--timeout="+timeout, "--owner="+owner)
	if result.ExitCode == 1 {
		return ErrLockHeld
	}
	return err
}

func (c Intercore) ReleaseCycleLock(ctx context.Context, portfolio, owner string) error {
	_, err := c.run(ctx, nil, "lock", "release", "remontoire-cycle", portfolio, "--owner="+owner)
	return err
}

func (c Intercore) SetCycle(ctx context.Context, cycle domain.Cycle) error {
	payload, err := json.Marshal(cycle)
	if err != nil {
		return fmt.Errorf("marshal cycle: %w", err)
	}
	_, err = c.run(ctx, payload, "state", "set", "remontoire.cycle", cycle.ID)
	return err
}

func (c Intercore) GetCycle(ctx context.Context, cycleID string) (domain.Cycle, error) {
	result, err := c.invoke(ctx, nil, "state", "get", "remontoire.cycle", cycleID)
	if result.ExitCode == 1 {
		return domain.Cycle{}, ErrNotFound
	}
	if err != nil {
		return domain.Cycle{}, err
	}
	var cycle domain.Cycle
	if err := json.Unmarshal(result.Stdout, &cycle); err != nil {
		return domain.Cycle{}, fmt.Errorf("decode cycle state: %w", err)
	}
	return cycle, nil
}

func (c Intercore) ListCycleIDs(ctx context.Context) ([]string, error) {
	result, err := c.run(ctx, nil, "state", "list", "remontoire.cycle")
	if err != nil {
		return nil, err
	}
	lines := strings.Fields(string(result.Stdout))
	return lines, nil
}

func (c Intercore) SetLatestCycle(ctx context.Context, portfolio, cycleID string) error {
	payload, err := json.Marshal(map[string]string{"cycle_id": cycleID})
	if err != nil {
		return err
	}
	_, err = c.run(ctx, payload, "state", "set", "remontoire.latest", portfolio)
	return err
}

func (c Intercore) GetLatestCycle(ctx context.Context, portfolio string) (string, error) {
	result, err := c.invoke(ctx, nil, "state", "get", "remontoire.latest", portfolio)
	if result.ExitCode == 1 {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	var payload struct {
		CycleID string `json:"cycle_id"`
	}
	if err := json.Unmarshal(result.Stdout, &payload); err != nil {
		return "", fmt.Errorf("decode latest cycle: %w", err)
	}
	if payload.CycleID == "" {
		return "", fmt.Errorf("latest cycle state has no cycle_id")
	}
	return payload.CycleID, nil
}

func (c Intercore) CreateCycleRun(ctx context.Context, project, cycleID string, metadata map[string]any) (string, error) {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal run metadata: %w", err)
	}
	if existing, err := c.findCycleRun(ctx, project, cycleID, metadataJSON); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	result, err := c.run(ctx, nil,
		"--json", "run", "create",
		"--project="+project,
		"--goal=Remontoire portfolio cycle "+cycleID,
		"--scope-id="+cycleID,
		`--phases=["observe","rank","propose","execute","review","compound"]`,
		"--metadata="+string(metadataJSON),
	)
	if err != nil {
		return c.reconcileCycleRun(ctx, project, cycleID, metadataJSON, err)
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result.Stdout, &payload); err != nil {
		return c.reconcileCycleRun(ctx, project, cycleID, metadataJSON, fmt.Errorf("decode run create: %w", err))
	}
	if payload.ID == "" {
		return c.reconcileCycleRun(ctx, project, cycleID, metadataJSON, fmt.Errorf("run create returned no id"))
	}
	return payload.ID, nil
}

func (c Intercore) reconcileCycleRun(ctx context.Context, project, cycleID string, metadataJSON []byte, cause error) (string, error) {
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	id, err := c.findCycleRun(lookupCtx, project, cycleID, metadataJSON)
	if err == nil {
		return id, nil
	}
	if errors.Is(err, ErrNotFound) {
		return "", cause
	}
	return "", errors.Join(cause, fmt.Errorf("reconcile run create: %w", err))
}

func (c Intercore) findCycleRun(ctx context.Context, project, cycleID string, metadataJSON []byte) (string, error) {
	result, err := c.run(ctx, nil, "--json", "run", "list", "--scope="+cycleID)
	if err != nil {
		return "", fmt.Errorf("list cycle runs: %w", err)
	}
	var runs []struct {
		ID         string   `json:"id"`
		ProjectDir string   `json:"project_dir"`
		Goal       string   `json:"goal"`
		Status     string   `json:"status"`
		Phase      string   `json:"phase"`
		ScopeID    string   `json:"scope_id"`
		Phases     []string `json:"phases"`
		Metadata   string   `json:"metadata"`
	}
	if err := json.Unmarshal(result.Stdout, &runs); err != nil {
		return "", fmt.Errorf("decode cycle run list: %w", err)
	}
	if len(runs) == 0 {
		return "", ErrNotFound
	}
	if len(runs) != 1 {
		return "", fmt.Errorf("cycle scope %q has %d runs", cycleID, len(runs))
	}
	run := runs[0]
	wantPhases := []string{"observe", "rank", "propose", "execute", "review", "compound"}
	var gotMetadata, wantMetadata any
	gotMetadataErr := json.Unmarshal([]byte(run.Metadata), &gotMetadata)
	wantMetadataErr := json.Unmarshal(metadataJSON, &wantMetadata)
	if run.ID == "" || run.ProjectDir != project || run.Goal != "Remontoire portfolio cycle "+cycleID || run.ScopeID != cycleID ||
		run.Status != "active" || run.Phase != "observe" || !reflect.DeepEqual(run.Phases, wantPhases) ||
		gotMetadataErr != nil || wantMetadataErr != nil || !reflect.DeepEqual(gotMetadata, wantMetadata) {
		return "", fmt.Errorf("cycle scope %q has a run with mismatched authority metadata", cycleID)
	}
	return run.ID, nil
}

func (c Intercore) AdvanceRun(ctx context.Context, runID string) error {
	_, err := c.run(ctx, nil, "run", "advance", runID, "--priority=1")
	return err
}

func (c Intercore) RunPhase(ctx context.Context, runID string) (string, error) {
	result, err := c.run(ctx, nil, "run", "phase", runID)
	if err != nil {
		return "", err
	}
	phase := strings.TrimSpace(string(result.Stdout))
	if phase == "" || strings.ContainsAny(phase, " \t\r\n") {
		return "", fmt.Errorf("run phase returned invalid phase %q", phase)
	}
	return phase, nil
}

func (c Intercore) RunStatus(ctx context.Context, runID string) (string, string, error) {
	result, err := c.run(ctx, nil, "--json", "run", "status", runID)
	if err != nil {
		return "", "", err
	}
	var payload struct {
		Phase  string `json:"phase"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(result.Stdout, &payload); err != nil {
		return "", "", fmt.Errorf("decode run status: %w", err)
	}
	if payload.Phase == "" || strings.ContainsAny(payload.Phase, " \t\r\n") || payload.Status == "" || strings.ContainsAny(payload.Status, " \t\r\n") {
		return "", "", fmt.Errorf("run status returned invalid phase/status %q/%q", payload.Phase, payload.Status)
	}
	return payload.Phase, payload.Status, nil
}

func (c Intercore) RecordReplayInput(ctx context.Context, runID, kind, key, payload, artifactRef string) error {
	args := []string{"run", "replay", "record", runID, "--kind=" + kind, "--key=" + key, "--payload=" + payload}
	if artifactRef != "" {
		args = append(args, "--artifact-ref="+artifactRef)
	}
	_, err := c.run(ctx, nil, args...)
	return err
}

func (c Intercore) RecordStageEvent(ctx context.Context, runID, project string, stage domain.Stage, cycleID string) error {
	contextJSON, err := json.Marshal(map[string]string{"cycle_id": cycleID, "stage": string(stage)})
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"agent_name": "remontoire", "context": string(contextJSON)})
	if err != nil {
		return err
	}
	_, err = c.run(ctx, nil,
		"events", "record",
		"--source=interspect",
		"--type=remontoire.stage",
		"--run="+runID,
		"--project="+project,
		"--idempotency-key=remontoire:"+cycleID+":"+string(stage),
		"--payload="+string(payload),
	)
	return err
}

func (c Intercore) Observation(ctx context.Context, limit int) ([]Discovery, InterestProfile, error) {
	result, err := c.run(ctx, nil, "--json", "discovery", "list", "--limit="+strconv.Itoa(limit))
	if err != nil {
		return nil, InterestProfile{}, err
	}
	var discoveries []Discovery
	if err := json.Unmarshal(result.Stdout, &discoveries); err != nil {
		return nil, InterestProfile{}, fmt.Errorf("decode discoveries: %w", err)
	}
	for i := range discoveries {
		id := strings.TrimSpace(discoveries[i].ID)
		if id == "" {
			return nil, InterestProfile{}, fmt.Errorf("discovery list returned an empty id")
		}
		result, err = c.run(ctx, nil, "--json", "discovery", "status", id)
		if err != nil {
			return nil, InterestProfile{}, fmt.Errorf("load discovery %q: %w", id, err)
		}
		var detail Discovery
		if err := json.Unmarshal(result.Stdout, &detail); err != nil {
			return nil, InterestProfile{}, fmt.Errorf("decode discovery %q: %w", id, err)
		}
		if detail.ID != id {
			return nil, InterestProfile{}, fmt.Errorf("discovery detail id %q does not match listed id %q", detail.ID, id)
		}
		discoveries[i] = detail
	}
	result, err = c.run(ctx, nil, "--json", "discovery", "profile")
	if err != nil {
		return nil, InterestProfile{}, err
	}
	var profile InterestProfile
	if err := json.Unmarshal(result.Stdout, &profile); err != nil {
		return nil, InterestProfile{}, fmt.Errorf("decode interest profile: %w", err)
	}
	return discoveries, profile, nil
}

func (c Intercore) SetOutcome(ctx context.Context, outcome domain.OutcomeSummary) error {
	payload, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("marshal outcome: %w", err)
	}
	_, err = c.run(ctx, payload, "state", "set", "remontoire.outcome", outcome.CycleID)
	return err
}

func (c Intercore) ListOutcomes(ctx context.Context, limit int) ([]domain.OutcomeSummary, error) {
	result, err := c.run(ctx, nil, "state", "list", "remontoire.outcome")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(result.Stdout))
	if limit > 0 && len(ids) > limit {
		ids = ids[len(ids)-limit:]
	}
	outcomes := make([]domain.OutcomeSummary, 0, len(ids))
	for _, id := range ids {
		result, err := c.run(ctx, nil, "state", "get", "remontoire.outcome", id)
		if err != nil {
			return nil, err
		}
		var outcome domain.OutcomeSummary
		if err := json.Unmarshal(result.Stdout, &outcome); err != nil {
			return nil, fmt.Errorf("decode outcome %s: %w", id, err)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func (c Intercore) RecordDiscoveryFeedback(ctx context.Context, discoveryID, signal, dataPath, idempotencyKey string) error {
	if strings.TrimSpace(idempotencyKey) == "" {
		return fmt.Errorf("record discovery feedback: idempotency key is required")
	}
	args := []string{
		"discovery", "feedback", discoveryID, "--signal=" + signal, "--actor=remontoire",
		"--idempotency-key=" + idempotencyKey,
	}
	if dataPath != "" {
		args = append(args, "--data=@"+dataPath)
	}
	_, err := c.run(ctx, nil, args...)
	return err
}

func (c Intercore) PromoteDiscovery(ctx context.Context, discoveryID, beadID string) error {
	_, err := c.run(ctx, nil, "discovery", "promote", discoveryID, "--bead-id="+beadID)
	return err
}

func (c Intercore) EmitReceipt(ctx context.Context, runID, model, contentHash string) (string, error) {
	result, err := c.run(ctx, nil,
		"--json", "receipt", "emit",
		"--agent=remontoire",
		"--model="+model,
		"--content-hash="+contentHash,
		"--parent-run="+runID,
	)
	if err != nil {
		return "", err
	}
	var payload struct {
		ReceiptID string `json:"receipt_id"`
	}
	if err := json.Unmarshal(result.Stdout, &payload); err != nil {
		return "", fmt.Errorf("decode receipt emit: %w", err)
	}
	if payload.ReceiptID == "" {
		return "", fmt.Errorf("receipt emit returned no receipt_id")
	}
	return payload.ReceiptID, nil
}

func (c Intercore) FindReceipt(ctx context.Context, runID, contentHash string) (string, error) {
	result, err := c.invoke(ctx, nil,
		"--json", "receipt", "find",
		"--agent=remontoire",
		"--parent-run="+runID,
		"--content-hash="+contentHash,
	)
	if result.ExitCode == 1 {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	var payload struct {
		ReceiptID string `json:"receipt_id"`
	}
	if err := json.Unmarshal(result.Stdout, &payload); err != nil {
		return "", fmt.Errorf("decode receipt find: %w", err)
	}
	if payload.ReceiptID == "" {
		return "", fmt.Errorf("receipt find returned no receipt_id")
	}
	return payload.ReceiptID, nil
}

func (c Intercore) VerifyReceipt(ctx context.Context, receiptID string) error {
	_, err := c.run(ctx, nil, "receipt", "verify", receiptID)
	return err
}

func (c Intercore) run(ctx context.Context, stdin []byte, args ...string) (Result, error) {
	return c.invoke(ctx, stdin, args...)
}

func (c Intercore) invoke(ctx context.Context, stdin []byte, args ...string) (Result, error) {
	if c.Runner == nil {
		return Result{}, fmt.Errorf("intercore runner is required")
	}
	binary := c.Binary
	if binary == "" {
		binary = "ic"
	}
	result, err := c.Runner.Run(ctx, Invocation{Name: binary, Args: args, Dir: c.Dir, Stdin: stdin})
	if err != nil || result.ExitCode != 0 {
		if err != nil {
			return result, fmt.Errorf("ic %s: %w", strings.Join(args[:min(2, len(args))], " "), err)
		}
		return result, fmt.Errorf("ic %s exited %d", strings.Join(args[:min(2, len(args))], " "), result.ExitCode)
	}
	return result, nil
}
