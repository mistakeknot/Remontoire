package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/cycle"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

const (
	ExitOK                 = 0
	ExitFailure            = 1
	ExitUsage              = 2
	ExitCanceled           = 130
	terminalCleanupTimeout = 15 * time.Second
)

type Service interface {
	Start(context.Context, domain.Mode) (domain.Cycle, error)
	Approve(context.Context, string, string) (domain.Cycle, error)
	Decline(context.Context, string, string, string) (domain.Cycle, error)
	ResumeObservation(context.Context, string) (domain.Cycle, error)
	ResumeProposal(context.Context, string) (domain.Cycle, error)
	Execute(context.Context, string) (domain.Cycle, error)
	Review(context.Context, string) (domain.Cycle, error)
	Compound(context.Context, string) (domain.Cycle, error)
}

type State interface {
	Health(context.Context) error
	GetCycle(context.Context, string) (domain.Cycle, error)
	GetLatestCycle(context.Context, string) (string, error)
}

type Backlog interface {
	ReadyPromotions(context.Context) ([]adapters.Bead, error)
}

type Application struct {
	Config   Config
	Service  Service
	State    State
	Backlog  Backlog
	Store    cycle.FileStore
	LookPath func(string) (string, error)
}

type DoctorCheck struct {
	Name     string `json:"name"`
	Healthy  bool   `json:"healthy"`
	Required bool   `json:"required"`
	Detail   string `json:"detail,omitempty"`
}

type DoctorReport struct {
	Healthy bool          `json:"healthy"`
	Checks  []DoctorCheck `json:"checks"`
}

type AttentionView struct {
	SchemaVersion string          `json:"schema_version"`
	Cycle         domain.Cycle    `json:"cycle"`
	Promotions    []adapters.Bead `json:"promotions"`
}

type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

func (a *Application) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	jsonMode, args := takeBool(args, "--json")
	if err := ctx.Err(); err != nil {
		return writeFailure(stderr, jsonMode, err, ExitCanceled)
	}
	a.Config.applyDefaults()
	if err := a.Config.Validate(); err != nil {
		return writeFailure(stderr, jsonMode, err, ExitUsage)
	}
	if a.Service == nil || a.State == nil {
		return writeFailure(stderr, jsonMode, fmt.Errorf("application service and state adapters are required"), ExitFailure)
	}
	if len(args) == 0 {
		return writeFailure(stderr, jsonMode, usageError{message: "usage: remontoire <cycle|approve|resume|decline|status|attention|receipt|doctor>"}, ExitUsage)
	}

	var value any
	var err error
	switch args[0] {
	case "cycle":
		value, err = a.runCycle(ctx, args[1:])
	case "approve":
		value, err = a.runApprove(ctx, args[1:])
	case "resume":
		value, err = a.runResume(ctx, args[1:])
	case "decline":
		value, err = a.runDecline(ctx, args[1:])
	case "status":
		value, err = a.runStatus(ctx, args[1:])
	case "attention":
		value, err = a.runAttention(ctx, args[1:])
	case "receipt":
		value, err = a.runReceipt(args[1:])
	case "doctor":
		if len(args) != 1 {
			err = usageError{message: "usage: remontoire doctor [--json]"}
		} else {
			value, err = a.Doctor(ctx)
		}
	default:
		err = usageError{message: "unknown command " + args[0]}
	}
	if err != nil {
		writePartial := false
		switch typed := value.(type) {
		case DoctorReport:
			writePartial = true
		case domain.Cycle:
			writePartial = typed.ID != ""
		}
		if writePartial {
			if writeErr := writeSuccess(stdout, jsonMode, value); writeErr != nil {
				return writeFailure(stderr, jsonMode, writeErr, ExitFailure)
			}
		}
		code := ExitFailure
		var usage usageError
		if errors.As(err, &usage) {
			code = ExitUsage
		} else if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = ExitCanceled
		}
		return writeFailure(stderr, jsonMode, err, code)
	}
	if err := writeSuccess(stdout, jsonMode, value); err != nil {
		return writeFailure(stderr, jsonMode, err, ExitFailure)
	}
	return ExitOK
}

func (a *Application) runAttention(ctx context.Context, args []string) (AttentionView, error) {
	if len(args) != 0 {
		return AttentionView{}, usageError{message: "usage: remontoire attention [--json]"}
	}
	if a.Backlog == nil {
		return AttentionView{}, fmt.Errorf("attention backlog adapter is required")
	}
	cycleValue, err := a.runStatus(ctx, nil)
	if err != nil {
		return AttentionView{}, err
	}
	promotions, err := a.Backlog.ReadyPromotions(ctx)
	if err != nil {
		return AttentionView{}, fmt.Errorf("read ready promotions: %w", err)
	}
	return AttentionView{
		SchemaVersion: "remontoire.attention/v1",
		Cycle:         cycleValue,
		Promotions:    promotions,
	}, nil
}

func (a *Application) runReceipt(args []string) (any, error) {
	if len(args) != 2 || strings.HasPrefix(args[1], "-") {
		return nil, usageError{message: "usage: remontoire receipt <show|replay> <cycle-id> [--json]"}
	}
	switch args[0] {
	case "show":
		return a.ShowReceipt(args[1])
	case "replay":
		return a.ReplayReceipt(args[1])
	default:
		return nil, usageError{message: "usage: remontoire receipt <show|replay> <cycle-id> [--json]"}
	}
}

func (a *Application) runCycle(ctx context.Context, args []string) (domain.Cycle, error) {
	modeValue, positionals, err := takeOption(args, "--mode")
	if err != nil || len(positionals) != 0 {
		return domain.Cycle{}, usageError{message: "usage: remontoire cycle [--mode=proposal|shadow] [--json]"}
	}
	mode := a.Config.DefaultMode
	if modeValue != "" {
		mode = domain.Mode(modeValue)
	}
	if mode != domain.ModeProposal && mode != domain.ModeShadow {
		return domain.Cycle{}, usageError{message: "cycle mode must be proposal or shadow"}
	}
	value, startErr := a.Service.Start(ctx, mode)
	if startErr != nil {
		return a.signFailedCycle(ctx, value, startErr)
	}
	if (value.Stage == domain.StageCompleted || value.Stage == domain.StageFailed) && value.SignedReceiptID == "" {
		return a.Service.Compound(ctx, value.ID)
	}
	return value, nil
}

func (a *Application) runApprove(ctx context.Context, args []string) (domain.Cycle, error) {
	actor, positionals, err := takeOption(args, "--actor")
	if err != nil || actor == "" || len(positionals) != 1 {
		return domain.Cycle{}, usageError{message: "usage: remontoire approve <cycle-id> --actor=<principal> [--json]"}
	}
	return a.Service.Approve(ctx, positionals[0], actor)
}

func (a *Application) runDecline(ctx context.Context, args []string) (domain.Cycle, error) {
	actor, args, actorErr := takeOption(args, "--actor")
	reason, positionals, reasonErr := takeOption(args, "--reason")
	if actorErr != nil || reasonErr != nil || actor == "" || reason == "" || len(positionals) != 1 {
		return domain.Cycle{}, usageError{message: "usage: remontoire decline <cycle-id> --actor=<principal> --reason=<text> [--json]"}
	}
	value, err := a.Service.Decline(ctx, positionals[0], actor, reason)
	if err != nil {
		return value, err
	}
	if value.SignedReceiptID == "" {
		return a.Service.Compound(ctx, value.ID)
	}
	return value, nil
}

func (a *Application) runResume(ctx context.Context, args []string) (domain.Cycle, error) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return domain.Cycle{}, usageError{message: "usage: remontoire resume <cycle-id> [--json]"}
	}
	value, err := a.State.GetCycle(ctx, args[0])
	if err != nil {
		return domain.Cycle{}, err
	}
	for step := 0; step < 12; step++ {
		switch value.Stage {
		case domain.StageNew, domain.StageObserving, domain.StageRanked:
			value, err = a.Service.ResumeObservation(ctx, value.ID)
		case domain.StageProposed:
			value, err = a.Service.ResumeProposal(ctx, value.ID)
		case domain.StageAwaitingApproval:
			return value, nil
		case domain.StageApproved, domain.StageExecuting:
			value, err = a.Service.Execute(ctx, value.ID)
		case domain.StageReviewing:
			value, err = a.Service.Review(ctx, value.ID)
		case domain.StageCompounding, domain.StageNoOp:
			value, err = a.Service.Compound(ctx, value.ID)
		case domain.StageFailed:
			if value.SignedReceiptID != "" {
				return value, nil
			}
			value, err = a.Service.Compound(ctx, value.ID)
		case domain.StageDeclined:
			if value.Decline == nil {
				return value, fmt.Errorf("declined cycle %s has no principal decision", value.ID)
			}
			value, err = a.Service.Decline(ctx, value.ID, value.Decline.Actor, value.Decline.Reason)
		case domain.StageCompleted:
			if value.SignedReceiptID == "" {
				value, err = a.Service.Compound(ctx, value.ID)
			} else {
				return value, nil
			}
		default:
			return value, fmt.Errorf("cycle %s cannot resume from stage %s", value.ID, value.Stage)
		}
		if err != nil {
			return a.signFailedCycle(ctx, value, err)
		}
	}
	return value, fmt.Errorf("cycle %s exceeded recovery transition limit", value.ID)
}

func (a *Application) runStatus(ctx context.Context, args []string) (domain.Cycle, error) {
	if len(args) > 1 || (len(args) == 1 && strings.HasPrefix(args[0], "-")) {
		return domain.Cycle{}, usageError{message: "usage: remontoire status [cycle-id] [--json]"}
	}
	id := ""
	if len(args) == 1 {
		id = args[0]
	} else {
		var err error
		id, err = a.State.GetLatestCycle(ctx, a.Config.Portfolio)
		if err != nil {
			return domain.Cycle{}, err
		}
	}
	return a.State.GetCycle(ctx, id)
}

func (a *Application) Doctor(ctx context.Context) (DoctorReport, error) {
	report := DoctorReport{Healthy: true}
	add := func(name string, required bool, err error) {
		check := DoctorCheck{Name: name, Required: required, Healthy: err == nil}
		if err != nil {
			check.Detail = err.Error()
			if required {
				report.Healthy = false
			}
		}
		report.Checks = append(report.Checks, check)
	}
	add("config", true, a.Config.Validate())
	healthErr := a.State.Health(ctx)
	add("intercore", true, healthErr)
	if errors.Is(healthErr, context.Canceled) || errors.Is(healthErr, context.DeadlineExceeded) {
		return report, healthErr
	}
	for _, check := range []struct{ name, path string }{
		{"judgment-schema", a.Config.JudgmentSchemaPath},
		{"execution-schema", a.Config.ExecutionSchemaPath},
		{"review-schema", a.Config.ReviewSchemaPath},
		{"roadmap-script", a.Config.RoadmapScriptPath},
	} {
		_, err := os.Stat(check.path)
		add(check.name, true, err)
	}
	lookPath := a.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	for _, binary := range []struct {
		name, value string
		required    bool
	}{
		{"ic", a.Config.IntercoreBinary, true}, {"bd", a.Config.BeadsBinary, true},
		{"git", a.Config.GitBinary, true}, {"bash", a.Config.BashBinary, true},
		{"codex", a.Config.CodexBinary, true}, {"claude", a.Config.ClaudeBinary, true},
		{"ockham", a.Config.OckhamBinary, false},
	} {
		_, err := lookPath(binary.value)
		add("binary:"+binary.name, binary.required, err)
	}
	if !report.Healthy {
		return report, fmt.Errorf("doctor found unhealthy required checks")
	}
	return report, nil
}

func (a *Application) signFailedCycle(ctx context.Context, value domain.Cycle, original error) (domain.Cycle, error) {
	if value.ID == "" || value.Stage != domain.StageFailed || value.SignedReceiptID != "" {
		return value, original
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalCleanupTimeout)
	defer cancel()
	signed, err := a.Service.Compound(cleanupCtx, value.ID)
	if err != nil {
		if signed.ID != "" {
			value = signed
		}
		return value, errors.Join(original, fmt.Errorf("sign failed cycle: %w", err))
	}
	return signed, original
}

func takeBool(args []string, name string) (bool, []string) {
	found := false
	result := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == name {
			found = true
			continue
		}
		result = append(result, arg)
	}
	return found, result
}

func takeOption(args []string, name string) (string, []string, error) {
	value := ""
	seen := false
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, name+"=") {
			if seen {
				return "", nil, fmt.Errorf("%s specified more than once", name)
			}
			seen = true
			value = strings.TrimPrefix(arg, name+"=")
			if value == "" {
				return "", nil, fmt.Errorf("%s requires one value", name)
			}
			continue
		}
		if arg == name {
			if seen || i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", nil, fmt.Errorf("%s requires one value", name)
			}
			seen = true
			i++
			value = args[i]
			continue
		}
		result = append(result, arg)
	}
	return value, result, nil
}

func writeSuccess(output io.Writer, jsonMode bool, value any) error {
	if jsonMode {
		return json.NewEncoder(output).Encode(value)
	}
	switch typed := value.(type) {
	case domain.Cycle:
		_, err := fmt.Fprintf(output, "%s\t%s\n", typed.ID, typed.Stage)
		return err
	case DoctorReport:
		status := "healthy"
		if !typed.Healthy {
			status = "unhealthy"
		}
		_, err := fmt.Fprintln(output, status)
		return err
	default:
		return json.NewEncoder(output).Encode(value)
	}
}

func writeFailure(output io.Writer, jsonMode bool, err error, code int) int {
	if jsonMode {
		_ = json.NewEncoder(output).Encode(map[string]any{"error": err.Error(), "exit_code": code})
	} else {
		_, _ = fmt.Fprintln(output, "remontoire:", err)
	}
	return code
}
