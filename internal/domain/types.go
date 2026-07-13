package domain

import "time"

const (
	CycleSchemaV1    = "remontoire.cycle/v1"
	ContractSchemaV1 = "remontoire.evidence-contract/v1"
	JudgmentSchemaV1 = "remontoire.judgment/v1"
	ApprovalSchemaV1 = "remontoire.approval/v1"
	DeclineSchemaV1  = "remontoire.decline/v1"
	ReviewSchemaV1   = "remontoire.review/v1"
	ReceiptSchemaV1  = "remontoire.receipt/v1"
	OutcomeSchemaV1  = "remontoire.outcome/v1"
)

type Mode string

const (
	ModeShadow   Mode = "shadow"
	ModeProposal Mode = "proposal"
)

type Stage string

const (
	StageNew              Stage = "new"
	StageObserving        Stage = "observing"
	StageRanked           Stage = "ranked"
	StageNoOp             Stage = "no_op"
	StageProposed         Stage = "proposed"
	StageAwaitingApproval Stage = "awaiting_approval"
	StageApproved         Stage = "approved"
	StageDeclined         Stage = "declined"
	StageExecuting        Stage = "executing"
	StageReviewing        Stage = "reviewing"
	StageCompounding      Stage = "compounding"
	StageCompleted        Stage = "completed"
	StageFailed           Stage = "failed"
)

type Direction string

const (
	DirectionMaximize Direction = "maximize"
	DirectionMinimize Direction = "minimize"
)

type MetricSource string

const (
	MetricSourceWallDurationMS MetricSource = "wall_duration_ms"
	MetricSourceStdoutJSON     MetricSource = "stdout_json"
)

type Verdict string

const (
	VerdictPromote      Verdict = "promote"
	VerdictCloseSuccess Verdict = "close_success"
	VerdictCloseFailure Verdict = "close_failure"
	VerdictInconclusive Verdict = "inconclusive"
)

type EvidenceRef struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

type Metric struct {
	Name      string       `json:"name"`
	Unit      string       `json:"unit"`
	Direction Direction    `json:"direction"`
	Source    MetricSource `json:"source"`
	JSONField string       `json:"json_field"`
	Baseline  float64      `json:"baseline"`
	Target    float64      `json:"target"`
}

type Budget struct {
	MaxDurationSeconds int     `json:"max_duration_seconds"`
	MaxTurns           int     `json:"max_turns"`
	MaxCostUSD         float64 `json:"max_cost_usd"`
}

type EvidenceContract struct {
	SchemaVersion     string   `json:"schema_version"`
	Hypothesis        string   `json:"hypothesis"`
	Falsifier         string   `json:"falsifier"`
	Repository        string   `json:"repository"`
	AllowedPaths      []string `json:"allowed_paths"`
	Metric            Metric   `json:"metric"`
	Benchmark         []string `json:"benchmark"`
	Budget            Budget   `json:"budget"`
	StopConditions    []string `json:"stop_conditions"`
	Executor          string   `json:"executor"`
	PromotionCriteria string   `json:"promotion_criteria"`
	ClosureCriteria   string   `json:"closure_criteria"`
	ArtifactPaths     []string `json:"artifact_paths"`
}

type Candidate struct {
	Title       string           `json:"title"`
	Summary     string           `json:"summary"`
	Project     string           `json:"project"`
	Priority    int              `json:"priority"`
	Impact      float64          `json:"impact"`
	Uncertainty float64          `json:"uncertainty"`
	Cost        float64          `json:"cost"`
	Risk        float64          `json:"risk"`
	PolicyFit   float64          `json:"policy_fit"`
	Evidence    []EvidenceRef    `json:"evidence"`
	Contract    EvidenceContract `json:"contract"`
}

type Judgment struct {
	SchemaVersion string      `json:"schema_version"`
	Opportunities []Candidate `json:"opportunities"`
	SelectedIndex *int        `json:"selected_index"`
	NoOpReason    string      `json:"no_op_reason"`
}

type Approval struct {
	SchemaVersion string    `json:"schema_version"`
	CycleID       string    `json:"cycle_id"`
	ContractHash  string    `json:"contract_hash"`
	Actor         string    `json:"actor"`
	ApprovedAt    time.Time `json:"approved_at"`
}

type Decline struct {
	SchemaVersion string    `json:"schema_version"`
	CycleID       string    `json:"cycle_id"`
	ContractHash  string    `json:"contract_hash"`
	Actor         string    `json:"actor"`
	Reason        string    `json:"reason"`
	DeclinedAt    time.Time `json:"declined_at"`
}

type Measurement struct {
	MetricName string        `json:"metric_name"`
	Value      float64       `json:"value"`
	ExitCode   int           `json:"exit_code"`
	Duration   time.Duration `json:"duration"`
	StdoutPath string        `json:"stdout_path,omitempty"`
	StderrPath string        `json:"stderr_path,omitempty"`
}

type ExecutionRecord struct {
	Backend      string    `json:"backend"`
	Model        string    `json:"model,omitempty"`
	WorktreePath string    `json:"worktree_path"`
	BaseCommit   string    `json:"base_commit"`
	StartedAt    time.Time `json:"started_at"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	ChangedPaths []string  `json:"changed_paths,omitempty"`
	Turns        int       `json:"turns,omitempty"`
	CostUSD      float64   `json:"cost_usd,omitempty"`
}

type Review struct {
	SchemaVersion string        `json:"schema_version"`
	ContractHash  string        `json:"contract_hash"`
	Verdict       Verdict       `json:"verdict"`
	Rationale     string        `json:"rationale"`
	Evidence      []EvidenceRef `json:"evidence"`
}

type ReviewResolution struct {
	ReviewerVerdict Verdict   `json:"reviewer_verdict"`
	FinalVerdict    Verdict   `json:"final_verdict"`
	OverrideReason  string    `json:"override_reason,omitempty"`
	ReviewerBackend string    `json:"reviewer_backend,omitempty"`
	ReviewerModel   string    `json:"reviewer_model,omitempty"`
	ResolvedAt      time.Time `json:"resolved_at"`
}

type OutcomeSummary struct {
	SchemaVersion    string    `json:"schema_version"`
	CycleID          string    `json:"cycle_id"`
	Portfolio        string    `json:"portfolio"`
	Project          string    `json:"project"`
	Title            string    `json:"title"`
	CandidateHash    string    `json:"candidate_hash"`
	ContractHash     string    `json:"contract_hash"`
	ExperimentBeadID string    `json:"experiment_bead_id"`
	PromotionBeadID  string    `json:"promotion_bead_id,omitempty"`
	FinalVerdict     Verdict   `json:"final_verdict"`
	MetricName       string    `json:"metric_name"`
	MetricValue      float64   `json:"metric_value"`
	MetricTarget     float64   `json:"metric_target"`
	MetricDirection  Direction `json:"metric_direction"`
	Rationale        string    `json:"rationale"`
	RecordedAt       time.Time `json:"recorded_at"`
}

type Artifact struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type Cycle struct {
	SchemaVersion    string            `json:"schema_version"`
	ID               string            `json:"id"`
	RunID            string            `json:"run_id,omitempty"`
	Portfolio        string            `json:"portfolio"`
	Mode             Mode              `json:"mode"`
	Stage            Stage             `json:"stage"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	Judgment         *Judgment         `json:"judgment,omitempty"`
	Candidate        *Candidate        `json:"candidate,omitempty"`
	CandidateHash    string            `json:"candidate_hash,omitempty"`
	ContractHash     string            `json:"contract_hash,omitempty"`
	ExperimentBeadID string            `json:"experiment_bead_id,omitempty"`
	PromotionBeadID  string            `json:"promotion_bead_id,omitempty"`
	Approval         *Approval         `json:"approval,omitempty"`
	Decline          *Decline          `json:"decline,omitempty"`
	Execution        *ExecutionRecord  `json:"execution,omitempty"`
	Measurement      *Measurement      `json:"measurement,omitempty"`
	Review           *Review           `json:"review,omitempty"`
	Resolution       *ReviewResolution `json:"resolution,omitempty"`
	Artifacts        []Artifact        `json:"artifacts,omitempty"`
	IdempotencyKeys  map[string]string `json:"idempotency_keys,omitempty"`
	RoadmapDigest    string            `json:"roadmap_digest,omitempty"`
	SignedReceiptID  string            `json:"signed_receipt_id,omitempty"`
	Failure          string            `json:"failure,omitempty"`
}

type Receipt struct {
	SchemaVersion   string     `json:"schema_version"`
	Cycle           Cycle      `json:"cycle"`
	InputArtifacts  []Artifact `json:"input_artifacts"`
	OutputArtifacts []Artifact `json:"output_artifacts"`
	DecisionHash    string     `json:"decision_hash"`
	TerminalAt      time.Time  `json:"terminal_at"`
}
