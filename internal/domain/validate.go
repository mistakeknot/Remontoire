package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	maxContractDurationSeconds = 3600
	maxContractTurns           = 30
	maxContractCostUSD         = 10.0
)

var permittedExecutors = map[string]bool{
	"claude":  true,
	"clavain": true,
	"codex":   true,
	"skaffen": true,
}

var permittedBenchmarks = map[string]bool{
	"bun":    true,
	"cargo":  true,
	"go":     true,
	"just":   true,
	"make":   true,
	"npm":    true,
	"pnpm":   true,
	"pytest": true,
	"uv":     true,
}

func ValidateEvidenceContract(c EvidenceContract) error {
	if c.SchemaVersion != ContractSchemaV1 {
		return fmt.Errorf("schema_version must be %q", ContractSchemaV1)
	}
	if blank(c.Hypothesis) {
		return fmt.Errorf("hypothesis is required")
	}
	if blank(c.Falsifier) {
		return fmt.Errorf("falsifier is required")
	}
	if !filepath.IsAbs(c.Repository) {
		return fmt.Errorf("repository must be an absolute path")
	}
	if filepath.Clean(c.Repository) != c.Repository {
		return fmt.Errorf("repository must be a clean absolute path")
	}
	if len(c.AllowedPaths) == 0 {
		return fmt.Errorf("allowed_paths must contain at least one path")
	}
	for _, allowed := range c.AllowedPaths {
		if err := validateRelativePath(allowed); err != nil {
			return fmt.Errorf("allowed_paths: %w", err)
		}
	}
	if err := validateMetric(c.Metric); err != nil {
		return err
	}
	if len(c.Benchmark) == 0 || blank(c.Benchmark[0]) {
		return fmt.Errorf("benchmark argv is required")
	}
	program := filepath.Base(strings.TrimSpace(c.Benchmark[0]))
	if !permittedBenchmarks[program] {
		return fmt.Errorf("benchmark program %q is not permitted", program)
	}
	for _, arg := range c.Benchmark {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("benchmark argv contains a control character")
		}
	}
	if c.Budget.MaxDurationSeconds < 1 || c.Budget.MaxDurationSeconds > maxContractDurationSeconds {
		return fmt.Errorf("max_duration_seconds must be between 1 and %d", maxContractDurationSeconds)
	}
	if c.Budget.MaxTurns < 1 || c.Budget.MaxTurns > maxContractTurns {
		return fmt.Errorf("max_turns must be between 1 and %d", maxContractTurns)
	}
	if math.IsNaN(c.Budget.MaxCostUSD) || math.IsInf(c.Budget.MaxCostUSD, 0) || c.Budget.MaxCostUSD <= 0 || c.Budget.MaxCostUSD > maxContractCostUSD {
		return fmt.Errorf("max_cost_usd must be greater than zero and at most %.2f", maxContractCostUSD)
	}
	if len(c.StopConditions) == 0 {
		return fmt.Errorf("stop_conditions must contain at least one condition")
	}
	if !permittedExecutors[strings.ToLower(strings.TrimSpace(c.Executor))] {
		return fmt.Errorf("executor %q is not permitted", c.Executor)
	}
	if blank(c.PromotionCriteria) {
		return fmt.Errorf("promotion_criteria is required")
	}
	if blank(c.ClosureCriteria) {
		return fmt.Errorf("closure_criteria is required")
	}
	for _, artifact := range c.ArtifactPaths {
		if err := validateRelativePath(artifact); err != nil {
			return fmt.Errorf("artifact_paths: %w", err)
		}
		if !coveredByAllowedPath(artifact, c.AllowedPaths) {
			return fmt.Errorf("artifact_paths: %q is outside allowed_paths", artifact)
		}
	}
	return nil
}

func validateMetric(m Metric) error {
	if blank(m.Name) {
		return fmt.Errorf("metric name is required")
	}
	if blank(m.Unit) {
		return fmt.Errorf("metric unit is required")
	}
	if !finite(m.Baseline) || !finite(m.Target) {
		return fmt.Errorf("metric baseline and target must be finite")
	}
	switch m.Source {
	case MetricSourceWallDurationMS:
		if m.Unit != "ms" {
			return fmt.Errorf("metric unit must be ms for wall_duration_ms")
		}
		if !blank(m.JSONField) {
			return fmt.Errorf("metric json_field must be empty for wall_duration_ms")
		}
	case MetricSourceStdoutJSON:
		if blank(m.JSONField) {
			return fmt.Errorf("metric json_field is required for stdout_json")
		}
		for _, r := range m.JSONField {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.') {
				return fmt.Errorf("metric json_field contains an invalid character")
			}
		}
	default:
		return fmt.Errorf("metric source must be %q or %q", MetricSourceWallDurationMS, MetricSourceStdoutJSON)
	}
	switch m.Direction {
	case DirectionMaximize:
		if m.Target <= m.Baseline {
			return fmt.Errorf("metric target must exceed baseline when maximizing")
		}
	case DirectionMinimize:
		if m.Target >= m.Baseline {
			return fmt.Errorf("metric target must be below baseline when minimizing")
		}
	default:
		return fmt.Errorf("metric direction must be %q or %q", DirectionMaximize, DirectionMinimize)
	}
	return nil
}

func validateRelativePath(value string) error {
	if blank(value) {
		return fmt.Errorf("path is empty")
	}
	if path.IsAbs(value) || filepath.IsAbs(value) {
		return fmt.Errorf("path %q must be relative", value)
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned != strings.TrimSuffix(value, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("path %q is not a clean repository-relative path", value)
	}
	if cleaned == ".git" || strings.HasPrefix(cleaned, ".git/") {
		return fmt.Errorf("path %q may not mutate git metadata", value)
	}
	if cleaned == ".github/workflows" || strings.HasPrefix(cleaned, ".github/workflows/") {
		return fmt.Errorf("path %q may not mutate production workflows", value)
	}
	return nil
}

func coveredByAllowedPath(value string, allowed []string) bool {
	cleaned := path.Clean(value)
	for _, prefix := range allowed {
		prefix = path.Clean(prefix)
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return true
		}
	}
	return false
}

func HashContract(c EvidenceContract) (string, error) {
	if err := ValidateEvidenceContract(c); err != nil {
		return "", err
	}
	normalized := normalizeContract(c)
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal canonical contract: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeContract(c EvidenceContract) EvidenceContract {
	c.Hypothesis = strings.TrimSpace(c.Hypothesis)
	c.Falsifier = strings.TrimSpace(c.Falsifier)
	c.Repository = filepath.Clean(c.Repository)
	c.AllowedPaths = normalizedSet(c.AllowedPaths)
	c.StopConditions = normalizedSet(c.StopConditions)
	c.ArtifactPaths = normalizedSet(c.ArtifactPaths)
	c.Executor = strings.ToLower(strings.TrimSpace(c.Executor))
	c.PromotionCriteria = strings.TrimSpace(c.PromotionCriteria)
	c.ClosureCriteria = strings.TrimSpace(c.ClosureCriteria)
	c.Metric.Name = strings.TrimSpace(c.Metric.Name)
	c.Metric.Unit = strings.TrimSpace(c.Metric.Unit)
	c.Benchmark = append([]string(nil), c.Benchmark...)
	for i := range c.Benchmark {
		c.Benchmark[i] = strings.TrimSpace(c.Benchmark[i])
	}
	return c
}

func normalizedSet(values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.TrimSpace(strings.TrimSuffix(value, "/"))
	}
	sort.Strings(result)
	return result
}

func FingerprintCandidate(c Candidate) string {
	key := normalizeWords(c.Contract.Repository) + "\x00" + normalizeWords(c.Title) + "\x00" + normalizeWords(c.Contract.Hypothesis)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func normalizeWords(value string) string {
	return strings.ToLower(strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " "))
}

func ValidateJudgment(j Judgment) error {
	if j.SchemaVersion != JudgmentSchemaV1 {
		return fmt.Errorf("schema_version must be %q", JudgmentSchemaV1)
	}
	if len(j.Opportunities) > 5 {
		return fmt.Errorf("judgment may contain at most five opportunities")
	}
	if j.SelectedIndex != nil && !blank(j.NoOpReason) {
		return fmt.Errorf("no_op_reason must be empty when selected_index is set")
	}
	if j.SelectedIndex == nil && blank(j.NoOpReason) {
		return fmt.Errorf("either selected_index or no_op_reason is required")
	}
	for i, candidate := range j.Opportunities {
		if err := validateCandidate(candidate); err != nil {
			return fmt.Errorf("opportunities[%d]: %w", i, err)
		}
	}
	if j.SelectedIndex != nil {
		if *j.SelectedIndex < 0 || *j.SelectedIndex >= len(j.Opportunities) {
			return fmt.Errorf("selected_index is outside opportunities")
		}
		if j.Opportunities[*j.SelectedIndex].Priority != 4 {
			return fmt.Errorf("selected opportunity must be P4")
		}
	}
	return nil
}

func validateCandidate(c Candidate) error {
	if blank(c.Title) {
		return fmt.Errorf("title is required")
	}
	if blank(c.Summary) {
		return fmt.Errorf("summary is required")
	}
	if blank(c.Project) {
		return fmt.Errorf("project is required")
	}
	if c.Priority != 4 {
		return fmt.Errorf("priority must be P4")
	}
	for name, score := range map[string]float64{
		"impact": c.Impact, "uncertainty": c.Uncertainty, "cost": c.Cost,
		"risk": c.Risk, "policy_fit": c.PolicyFit,
	} {
		if !finite(score) || score < 0 || score > 1 {
			return fmt.Errorf("%s score must be between 0 and 1", name)
		}
	}
	if len(c.Evidence) == 0 {
		return fmt.Errorf("evidence must contain at least one reference")
	}
	for i, ref := range c.Evidence {
		if blank(ref.Kind) || blank(ref.ID) || !validDigest(ref.Digest) {
			return fmt.Errorf("evidence[%d] must have kind, id, and SHA-256 digest", i)
		}
	}
	return ValidateEvidenceContract(c.Contract)
}

func ValidateApproval(a Approval, cycleID string, contract EvidenceContract) error {
	if a.SchemaVersion != ApprovalSchemaV1 {
		return fmt.Errorf("schema_version must be %q", ApprovalSchemaV1)
	}
	if blank(a.CycleID) || a.CycleID != cycleID {
		return fmt.Errorf("cycle_id does not match the current cycle")
	}
	expected, err := HashContract(contract)
	if err != nil {
		return fmt.Errorf("contract: %w", err)
	}
	if a.ContractHash != expected {
		return fmt.Errorf("contract_hash does not match the approved contract")
	}
	actor := strings.ToLower(strings.TrimSpace(a.Actor))
	if actor == "" || actor == "remontoire" {
		return fmt.Errorf("actor must identify an external principal")
	}
	if a.ApprovedAt.IsZero() {
		return fmt.Errorf("approved_at is required")
	}
	return nil
}

func ValidateTransition(from, to Stage) error {
	if to == StageFailed && activeStage(from) {
		return nil
	}
	allowed := map[Stage]map[Stage]bool{
		StageNew:              {StageObserving: true},
		StageObserving:        {StageRanked: true},
		StageRanked:           {StageNoOp: true, StageProposed: true},
		StageNoOp:             {StageCompleted: true},
		StageProposed:         {StageAwaitingApproval: true},
		StageAwaitingApproval: {StageApproved: true, StageDeclined: true},
		StageApproved:         {StageExecuting: true},
		StageDeclined:         {StageCompleted: true},
		StageExecuting:        {StageReviewing: true},
		StageReviewing:        {StageCompounding: true},
		StageCompounding:      {StageCompleted: true},
	}
	if !allowed[from][to] {
		return fmt.Errorf("transition %s -> %s is not allowed", from, to)
	}
	return nil
}

func activeStage(stage Stage) bool {
	return stage != StageCompleted && stage != StageFailed && stage != StageDeclined && stage != StageNoOp
}

func ValidatePromotion(contract EvidenceContract, measurement Measurement, review Review) error {
	if err := ValidateEvidenceContract(contract); err != nil {
		return err
	}
	hash, err := HashContract(contract)
	if err != nil {
		return err
	}
	if review.SchemaVersion != ReviewSchemaV1 {
		return fmt.Errorf("review schema_version must be %q", ReviewSchemaV1)
	}
	if review.ContractHash != hash {
		return fmt.Errorf("review contract_hash does not match")
	}
	if review.Verdict != VerdictPromote {
		return fmt.Errorf("review verdict must be %q", VerdictPromote)
	}
	if blank(review.Rationale) || len(review.Evidence) == 0 {
		return fmt.Errorf("review rationale and evidence are required")
	}
	if measurement.MetricName != contract.Metric.Name {
		return fmt.Errorf("measurement metric does not match contract")
	}
	if measurement.ExitCode != 0 {
		return fmt.Errorf("benchmark exit status was %d", measurement.ExitCode)
	}
	if !finite(measurement.Value) {
		return fmt.Errorf("measurement value must be finite")
	}
	met := false
	switch contract.Metric.Direction {
	case DirectionMaximize:
		met = measurement.Value >= contract.Metric.Target
	case DirectionMinimize:
		met = measurement.Value <= contract.Metric.Target
	}
	if !met {
		return fmt.Errorf("measurement did not reach the contract target")
	}
	return nil
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
