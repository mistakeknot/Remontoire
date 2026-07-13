package domain

import "time"

const (
	CycleSchemaV1    = "remontoire.cycle/v1"
	ContractSchemaV1 = "remontoire.evidence-contract/v1"
	JudgmentSchemaV1 = "remontoire.judgment/v1"
	ApprovalSchemaV1 = "remontoire.approval/v1"
	ReviewSchemaV1   = "remontoire.review/v1"
	ReceiptSchemaV1  = "remontoire.receipt/v1"
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
	Name      string    `json:"name"`
	Unit      string    `json:"unit"`
	Direction Direction `json:"direction"`
	Baseline  float64   `json:"baseline"`
	Target    float64   `json:"target"`
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

type Measurement struct {
	MetricName string        `json:"metric_name"`
	Value      float64       `json:"value"`
	ExitCode   int           `json:"exit_code"`
	Duration   time.Duration `json:"duration"`
	StdoutPath string        `json:"stdout_path,omitempty"`
	StderrPath string        `json:"stderr_path,omitempty"`
}

type Review struct {
	SchemaVersion string        `json:"schema_version"`
	ContractHash  string        `json:"contract_hash"`
	Verdict       Verdict       `json:"verdict"`
	Rationale     string        `json:"rationale"`
	Evidence      []EvidenceRef `json:"evidence"`
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
	Measurement      *Measurement      `json:"measurement,omitempty"`
	Review           *Review           `json:"review,omitempty"`
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
