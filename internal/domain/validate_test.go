package domain

import (
	"strings"
	"testing"
	"time"
)

func validContract() EvidenceContract {
	return EvidenceContract{
		SchemaVersion: ContractSchemaV1,
		Hypothesis:    "Caching the parsed roadmap reduces median refresh time by at least 20 percent.",
		Falsifier:     "The measured median does not improve by the target or correctness tests fail.",
		Repository:    "/home/mk/projects/Sylveste/os/Remontoire",
		AllowedPaths:  []string{"internal/roadmap", "docs/benchmarks"},
		Metric: Metric{
			Name:      "median_refresh_ms",
			Unit:      "ms",
			Direction: DirectionMinimize,
			Source:    MetricSourceWallDurationMS,
			Baseline:  100,
			Target:    80,
		},
		Benchmark: []string{"go", "test", "./internal/roadmap", "-run", "TestRefreshBenchmark"},
		Budget: Budget{
			MaxDurationSeconds: 900,
			MaxTurns:           8,
			MaxCostUSD:         3,
		},
		StopConditions:    []string{"benchmark exits non-zero", "diff leaves allowed paths"},
		Executor:          "codex",
		PromotionCriteria: "primary metric reaches target and all tests pass",
		ClosureCriteria:   "target is missed or correctness regresses",
		ArtifactPaths:     []string{"docs/benchmarks/roadmap.json"},
	}
}

func validCandidate() Candidate {
	return Candidate{
		Title:       "Cache parsed roadmap during refresh",
		Summary:     "Retire uncertainty about repeated parse overhead.",
		Project:     "Remontoire",
		Priority:    4,
		Impact:      0.7,
		Uncertainty: 0.8,
		Cost:        0.2,
		Risk:        0.2,
		PolicyFit:   0.9,
		Evidence: []EvidenceRef{
			{Kind: "bead", ID: "Revel-fixture", Digest: strings.Repeat("a", 64)},
		},
		Contract: validContract(),
	}
}

func TestValidateEvidenceContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EvidenceContract)
		want   string
	}{
		{name: "valid"},
		{name: "missing hypothesis", mutate: func(c *EvidenceContract) { c.Hypothesis = "" }, want: "hypothesis"},
		{name: "missing falsifier", mutate: func(c *EvidenceContract) { c.Falsifier = "" }, want: "falsifier"},
		{name: "relative repository", mutate: func(c *EvidenceContract) { c.Repository = "Sylveste" }, want: "absolute"},
		{name: "parent traversal", mutate: func(c *EvidenceContract) { c.AllowedPaths = []string{"../Intercore"} }, want: "allowed_paths"},
		{name: "git metadata", mutate: func(c *EvidenceContract) { c.AllowedPaths = []string{".git/hooks"} }, want: "allowed_paths"},
		{name: "workflow mutation", mutate: func(c *EvidenceContract) { c.AllowedPaths = []string{".github/workflows"} }, want: "allowed_paths"},
		{name: "unbounded duration", mutate: func(c *EvidenceContract) { c.Budget.MaxDurationSeconds = 0 }, want: "max_duration_seconds"},
		{name: "excessive duration", mutate: func(c *EvidenceContract) { c.Budget.MaxDurationSeconds = 3601 }, want: "max_duration_seconds"},
		{name: "unbounded turns", mutate: func(c *EvidenceContract) { c.Budget.MaxTurns = 0 }, want: "max_turns"},
		{name: "excessive cost", mutate: func(c *EvidenceContract) { c.Budget.MaxCostUSD = 10.01 }, want: "max_cost_usd"},
		{name: "wrong target direction", mutate: func(c *EvidenceContract) { c.Metric.Target = 120 }, want: "target"},
		{name: "missing metric source", mutate: func(c *EvidenceContract) { c.Metric.Source = "" }, want: "source"},
		{name: "json source without field", mutate: func(c *EvidenceContract) { c.Metric.Source = MetricSourceStdoutJSON }, want: "json_field"},
		{name: "shell benchmark", mutate: func(c *EvidenceContract) { c.Benchmark = []string{"bash", "-c", "go test ./..."} }, want: "benchmark"},
		{name: "git benchmark", mutate: func(c *EvidenceContract) { c.Benchmark = []string{"git", "push"} }, want: "benchmark"},
		{name: "unknown executor", mutate: func(c *EvidenceContract) { c.Executor = "unbounded-agent" }, want: "executor"},
		{name: "missing stop condition", mutate: func(c *EvidenceContract) { c.StopConditions = nil }, want: "stop_conditions"},
		{name: "missing promotion criteria", mutate: func(c *EvidenceContract) { c.PromotionCriteria = "" }, want: "promotion_criteria"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := validContract()
			if tt.mutate != nil {
				tt.mutate(&contract)
			}
			err := ValidateEvidenceContract(contract)
			if tt.want == "" && err != nil {
				t.Fatalf("ValidateEvidenceContract() error = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("ValidateEvidenceContract() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestHashContractIsCanonical(t *testing.T) {
	a := validContract()
	b := validContract()
	b.AllowedPaths = []string{"docs/benchmarks", "internal/roadmap"}
	b.StopConditions = []string{"diff leaves allowed paths", "benchmark exits non-zero"}

	ha, err := HashContract(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HashContract(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("semantically equal contracts have different hashes: %s != %s", ha, hb)
	}

	b.Hypothesis += " Changed."
	hc, err := HashContract(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha == hc {
		t.Fatal("materially different contracts have the same hash")
	}
}

func TestCandidateFingerprintIsStable(t *testing.T) {
	a := validCandidate()
	b := a
	b.Title = "  CACHE   parsed ROADMAP during refresh "
	b.Contract.Hypothesis = "  CACHING the parsed roadmap reduces median refresh time by at least 20 percent. "

	if FingerprintCandidate(a) != FingerprintCandidate(b) {
		t.Fatal("fingerprint should ignore case and repeated whitespace")
	}
	b.Contract.Repository = "/home/mk/projects/Sylveste/core/intercore"
	if FingerprintCandidate(a) == FingerprintCandidate(b) {
		t.Fatal("fingerprint should include repository")
	}
}

func TestValidateJudgment(t *testing.T) {
	selected := 0
	valid := Judgment{
		SchemaVersion: JudgmentSchemaV1,
		Opportunities: []Candidate{validCandidate()},
		SelectedIndex: &selected,
	}
	if err := ValidateJudgment(valid); err != nil {
		t.Fatalf("valid judgment: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Judgment)
		want   string
	}{
		{name: "too many", mutate: func(j *Judgment) { j.Opportunities = make([]Candidate, 6) }, want: "five"},
		{name: "selected out of range", mutate: func(j *Judgment) { i := 4; j.SelectedIndex = &i }, want: "selected_index"},
		{name: "non p4", mutate: func(j *Judgment) { j.Opportunities[0].Priority = 3 }, want: "P4"},
		{name: "missing evidence", mutate: func(j *Judgment) { j.Opportunities[0].Evidence = nil }, want: "evidence"},
		{name: "selection and no-op", mutate: func(j *Judgment) { j.NoOpReason = "nothing useful" }, want: "no_op_reason"},
		{name: "neither selection nor no-op", mutate: func(j *Judgment) { j.SelectedIndex = nil }, want: "selected_index"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := valid
			j.Opportunities = append([]Candidate(nil), valid.Opportunities...)
			tt.mutate(&j)
			err := ValidateJudgment(j)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateJudgment() error = %v, want substring %q", err, tt.want)
			}
		})
	}

	noOp := Judgment{SchemaVersion: JudgmentSchemaV1, NoOpReason: "No evidence-backed bounded opportunity."}
	if err := ValidateJudgment(noOp); err != nil {
		t.Fatalf("valid no-op judgment: %v", err)
	}
}

func TestValidateApproval(t *testing.T) {
	contract := validContract()
	hash, err := HashContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	approval := Approval{
		SchemaVersion: ApprovalSchemaV1,
		CycleID:       "cycle-20260713T073000Z-abcd1234",
		ContractHash:  hash,
		Actor:         "mk",
		ApprovedAt:    time.Date(2026, 7, 13, 7, 35, 0, 0, time.UTC),
	}
	if err := ValidateApproval(approval, approval.CycleID, contract); err != nil {
		t.Fatalf("valid approval: %v", err)
	}

	stale := approval
	stale.ContractHash = strings.Repeat("b", 64)
	if err := ValidateApproval(stale, approval.CycleID, contract); err == nil || !strings.Contains(err.Error(), "contract_hash") {
		t.Fatalf("stale approval error = %v", err)
	}

	self := approval
	self.Actor = "remontoire"
	if err := ValidateApproval(self, approval.CycleID, contract); err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("self approval error = %v", err)
	}
}

func TestStageTransitions(t *testing.T) {
	allowed := [][2]Stage{
		{StageNew, StageObserving},
		{StageObserving, StageRanked},
		{StageRanked, StageProposed},
		{StageRanked, StageNoOp},
		{StageProposed, StageAwaitingApproval},
		{StageAwaitingApproval, StageApproved},
		{StageAwaitingApproval, StageDeclined},
		{StageApproved, StageExecuting},
		{StageExecuting, StageReviewing},
		{StageReviewing, StageCompounding},
		{StageCompounding, StageCompleted},
		{StageNoOp, StageCompleted},
		{StageDeclined, StageCompleted},
	}
	for _, pair := range allowed {
		if err := ValidateTransition(pair[0], pair[1]); err != nil {
			t.Errorf("transition %s -> %s: %v", pair[0], pair[1], err)
		}
	}

	for _, pair := range [][2]Stage{
		{StageNew, StageExecuting},
		{StageAwaitingApproval, StageExecuting},
		{StageCompleted, StageObserving},
		{StageFailed, StageExecuting},
	} {
		if err := ValidateTransition(pair[0], pair[1]); err == nil {
			t.Errorf("transition %s -> %s unexpectedly allowed", pair[0], pair[1])
		}
	}

	if err := ValidateTransition(StageObserving, StageFailed); err != nil {
		t.Fatalf("active -> failed should be allowed: %v", err)
	}
}

func TestPromotionRequiresMeasuredAndReviewedSuccess(t *testing.T) {
	contract := validContract()
	hash, err := HashContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	measurement := Measurement{
		MetricName: contract.Metric.Name,
		Value:      75,
		ExitCode:   0,
	}
	review := Review{
		SchemaVersion: ReviewSchemaV1,
		ContractHash:  hash,
		Verdict:       VerdictPromote,
		Rationale:     "The independently measured target passed and the diff is bounded.",
		Evidence:      []EvidenceRef{{Kind: "measurement", ID: "metric.json", Digest: strings.Repeat("c", 64)}},
	}
	if err := ValidatePromotion(contract, measurement, review); err != nil {
		t.Fatalf("valid promotion: %v", err)
	}

	badMetric := measurement
	badMetric.Value = 90
	if err := ValidatePromotion(contract, badMetric, review); err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("missed target error = %v", err)
	}

	failed := measurement
	failed.ExitCode = 1
	if err := ValidatePromotion(contract, failed, review); err == nil || !strings.Contains(err.Error(), "exit") {
		t.Fatalf("failed benchmark error = %v", err)
	}

	inconclusive := review
	inconclusive.Verdict = VerdictInconclusive
	if err := ValidatePromotion(contract, measurement, inconclusive); err == nil || !strings.Contains(err.Error(), "verdict") {
		t.Fatalf("inconclusive review error = %v", err)
	}
}
