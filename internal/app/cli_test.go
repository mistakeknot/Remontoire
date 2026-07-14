package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/cycle"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

type fakeEngine struct {
	current             domain.Cycle
	latest              string
	serviceCalls        []string
	stateCalls          []string
	backlogCalls        []string
	promotions          []adapters.Bead
	startMode           domain.Mode
	err                 error
	startErr            error
	executeErr          error
	reviewErr           error
	compoundErr         error
	compoundHasDeadline bool
	onHealth            func()
}

func (f *fakeEngine) Start(_ context.Context, mode domain.Mode) (domain.Cycle, error) {
	f.serviceCalls = append(f.serviceCalls, "cycle")
	f.startMode = mode
	if f.startErr != nil {
		return f.current, f.startErr
	}
	if f.err != nil {
		return domain.Cycle{}, f.err
	}
	f.current = domain.Cycle{SchemaVersion: domain.CycleSchemaV1, ID: "cycle-1", Portfolio: "sylveste", Mode: mode, Stage: domain.StageAwaitingApproval}
	return f.current, nil
}
func (f *fakeEngine) Approve(_ context.Context, id, actor string) (domain.Cycle, error) {
	f.serviceCalls = append(f.serviceCalls, "approve:"+id+":"+actor)
	if f.err != nil {
		return f.current, f.err
	}
	f.current.Stage = domain.StageApproved
	return f.current, nil
}
func (f *fakeEngine) Decline(_ context.Context, id, actor, reason string) (domain.Cycle, error) {
	f.serviceCalls = append(f.serviceCalls, "decline:"+id+":"+actor+":"+reason)
	if f.err != nil {
		return f.current, f.err
	}
	f.current.Stage = domain.StageCompleted
	return f.current, nil
}
func (f *fakeEngine) ResumeObservation(_ context.Context, id string) (domain.Cycle, error) {
	f.serviceCalls = append(f.serviceCalls, "observation:"+id)
	if f.err != nil {
		return f.current, f.err
	}
	f.current.Stage = domain.StageAwaitingApproval
	return f.current, nil
}
func (f *fakeEngine) ResumeProposal(_ context.Context, id string) (domain.Cycle, error) {
	f.serviceCalls = append(f.serviceCalls, "proposal:"+id)
	f.current.Stage = domain.StageAwaitingApproval
	return f.current, f.err
}
func (f *fakeEngine) Execute(_ context.Context, id string) (domain.Cycle, error) {
	f.serviceCalls = append(f.serviceCalls, "execute:"+id)
	if f.executeErr != nil {
		f.current.Stage = domain.StageFailed
		return f.current, f.executeErr
	}
	if f.err != nil {
		return f.current, f.err
	}
	f.current.Stage = domain.StageReviewing
	return f.current, nil
}
func (f *fakeEngine) Review(_ context.Context, id string) (domain.Cycle, error) {
	f.serviceCalls = append(f.serviceCalls, "review:"+id)
	if f.reviewErr != nil {
		f.current.Stage = domain.StageFailed
		return f.current, f.reviewErr
	}
	if f.err != nil {
		return f.current, f.err
	}
	f.current.Stage = domain.StageCompounding
	return f.current, nil
}
func (f *fakeEngine) Compound(ctx context.Context, id string) (domain.Cycle, error) {
	f.serviceCalls = append(f.serviceCalls, "compound:"+id)
	_, f.compoundHasDeadline = ctx.Deadline()
	if f.compoundErr != nil {
		return f.current, f.compoundErr
	}
	if f.err != nil {
		return f.current, f.err
	}
	if f.current.Stage != domain.StageFailed {
		f.current.Stage = domain.StageCompleted
	}
	f.current.SignedReceiptID = "receipt-1"
	return f.current, nil
}
func (f *fakeEngine) Health(context.Context) error {
	f.stateCalls = append(f.stateCalls, "health")
	if f.onHealth != nil {
		f.onHealth()
	}
	return f.err
}
func (f *fakeEngine) GetCycle(_ context.Context, id string) (domain.Cycle, error) {
	f.stateCalls = append(f.stateCalls, "get:"+id)
	if f.err != nil {
		return domain.Cycle{}, f.err
	}
	return f.current, nil
}
func (f *fakeEngine) GetLatestCycle(_ context.Context, portfolio string) (string, error) {
	f.stateCalls = append(f.stateCalls, "latest:"+portfolio)
	if f.err != nil {
		return "", f.err
	}
	return f.latest, nil
}
func (f *fakeEngine) ReadyPromotions(context.Context) ([]adapters.Bead, error) {
	f.backlogCalls = append(f.backlogCalls, "ready-promotions")
	if f.err != nil {
		return nil, f.err
	}
	return append([]adapters.Bead(nil), f.promotions...), nil
}

func testApplication(t *testing.T, engine *fakeEngine) *Application {
	t.Helper()
	cfg := validConfig(t)
	return &Application{
		Config: cfg, Service: engine, State: engine, Backlog: engine,
		Store:    cycle.FileStore{Root: cfg.ArtifactRoot},
		LookPath: func(binary string) (string, error) { return filepath.Join("/usr/bin", filepath.Base(binary)), nil },
	}
}

func TestCLIAttentionReturnsLatestCycleAndReadyPromotionsWithoutServiceCalls(t *testing.T) {
	engine := &fakeEngine{
		latest: "cycle-1",
		current: domain.Cycle{
			SchemaVersion: domain.CycleSchemaV1,
			ID:            "cycle-1",
			Portfolio:     "sylveste",
			Mode:          domain.ModeProposal,
			Stage:         domain.StageAwaitingApproval,
		},
		promotions: []adapters.Bead{{
			ID: "Revel-prom", Title: "Land measured cache improvement", Status: "open",
			Priority: 2, IssueType: "feature", DependentCount: 3,
			Labels: []string{"remontoire-promotion", "remontoire:cycle:cycle-1"},
		}},
	}

	code, output, stderr := runJSON(t, testApplication(t, engine), "attention", "--json")
	if code != ExitOK || stderr != "" {
		t.Fatalf("code=%d output=%#v stderr=%q", code, output, stderr)
	}
	if output["schema_version"] != "remontoire.attention/v1" {
		t.Fatalf("schema = %#v", output["schema_version"])
	}
	cycleValue, ok := output["cycle"].(map[string]any)
	if !ok || cycleValue["id"] != "cycle-1" || cycleValue["stage"] != "awaiting_approval" {
		t.Fatalf("cycle = %#v", output["cycle"])
	}
	promotions, ok := output["promotions"].([]any)
	if !ok || len(promotions) != 1 || promotions[0].(map[string]any)["id"] != "Revel-prom" {
		t.Fatalf("promotions = %#v", output["promotions"])
	}
	if len(engine.serviceCalls) != 0 {
		t.Fatalf("attention called cycle service: %#v", engine.serviceCalls)
	}
	if !reflect.DeepEqual(engine.stateCalls, []string{"latest:sylveste", "get:cycle-1"}) ||
		!reflect.DeepEqual(engine.backlogCalls, []string{"ready-promotions"}) {
		t.Fatalf("state=%#v backlog=%#v", engine.stateCalls, engine.backlogCalls)
	}
}

func runJSON(t *testing.T, application *Application, args ...string) (int, map[string]any, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := application.Run(context.Background(), args, &stdout, &stderr)
	value := map[string]any{}
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
			t.Fatalf("decode stdout %q: %v", stdout.String(), err)
		}
	}
	return code, value, stderr.String()
}

func TestCLICommandsRouteStagesAndEmitStructuredJSON(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		engine := &fakeEngine{}
		code, output, stderr := runJSON(t, testApplication(t, engine), "cycle", "--mode=shadow", "--json")
		if code != 0 || stderr != "" || engine.startMode != domain.ModeShadow || output["id"] != "cycle-1" {
			t.Fatalf("code=%d output=%#v stderr=%q mode=%s", code, output, stderr, engine.startMode)
		}
	})

	t.Run("approve", func(t *testing.T) {
		engine := &fakeEngine{current: domain.Cycle{ID: "cycle-1", Stage: domain.StageAwaitingApproval}}
		code, _, stderr := runJSON(t, testApplication(t, engine), "approve", "cycle-1", "--actor=mk", "--json")
		if code != 0 || stderr != "" || !reflect.DeepEqual(engine.serviceCalls, []string{"approve:cycle-1:mk"}) {
			t.Fatalf("code=%d stderr=%q calls=%#v", code, stderr, engine.serviceCalls)
		}
	})

	t.Run("resume", func(t *testing.T) {
		engine := &fakeEngine{current: domain.Cycle{ID: "cycle-1", Stage: domain.StageApproved}}
		code, output, stderr := runJSON(t, testApplication(t, engine), "resume", "cycle-1", "--json")
		want := []string{"execute:cycle-1", "review:cycle-1", "compound:cycle-1"}
		if code != 0 || stderr != "" || output["signed_receipt_id"] != "receipt-1" || !reflect.DeepEqual(engine.serviceCalls, want) {
			t.Fatalf("code=%d output=%#v stderr=%q calls=%#v", code, output, stderr, engine.serviceCalls)
		}
	})

	for _, stage := range []domain.Stage{domain.StageNew, domain.StageObserving, domain.StageRanked} {
		t.Run("resume "+string(stage), func(t *testing.T) {
			engine := &fakeEngine{current: domain.Cycle{ID: "cycle-1", Stage: stage}}
			code, output, stderr := runJSON(t, testApplication(t, engine), "resume", "cycle-1", "--json")
			want := []string{"observation:cycle-1"}
			if code != 0 || stderr != "" || output["stage"] != string(domain.StageAwaitingApproval) || !reflect.DeepEqual(engine.serviceCalls, want) {
				t.Fatalf("code=%d output=%#v stderr=%q calls=%#v", code, output, stderr, engine.serviceCalls)
			}
		})
	}

	t.Run("resume failed", func(t *testing.T) {
		engine := &fakeEngine{current: domain.Cycle{ID: "cycle-1", Stage: domain.StageFailed}}
		code, output, stderr := runJSON(t, testApplication(t, engine), "resume", "cycle-1", "--json")
		want := []string{"compound:cycle-1"}
		if code != 0 || stderr != "" || output["signed_receipt_id"] != "receipt-1" || output["stage"] != string(domain.StageFailed) || !reflect.DeepEqual(engine.serviceCalls, want) {
			t.Fatalf("code=%d output=%#v stderr=%q calls=%#v", code, output, stderr, engine.serviceCalls)
		}
	})

	t.Run("resume declined", func(t *testing.T) {
		engine := &fakeEngine{current: domain.Cycle{
			ID: "cycle-1", Stage: domain.StageDeclined,
			Decline: &domain.Decline{Actor: "mk", Reason: "low leverage"},
		}}
		code, output, stderr := runJSON(t, testApplication(t, engine), "resume", "cycle-1", "--json")
		want := []string{"decline:cycle-1:mk:low leverage", "compound:cycle-1"}
		if code != 0 || stderr != "" || output["signed_receipt_id"] != "receipt-1" || !reflect.DeepEqual(engine.serviceCalls, want) {
			t.Fatalf("code=%d output=%#v stderr=%q calls=%#v", code, output, stderr, engine.serviceCalls)
		}
	})

	t.Run("decline", func(t *testing.T) {
		engine := &fakeEngine{current: domain.Cycle{ID: "cycle-1", Stage: domain.StageAwaitingApproval}}
		code, _, stderr := runJSON(t, testApplication(t, engine), "decline", "cycle-1", "--actor", "mk", "--reason", "low leverage", "--json")
		want := []string{"decline:cycle-1:mk:low leverage", "compound:cycle-1"}
		if code != 0 || stderr != "" || !reflect.DeepEqual(engine.serviceCalls, want) {
			t.Fatalf("code=%d stderr=%q calls=%#v", code, stderr, engine.serviceCalls)
		}
	})

	t.Run("status latest", func(t *testing.T) {
		engine := &fakeEngine{latest: "cycle-1", current: domain.Cycle{ID: "cycle-1", Stage: domain.StageAwaitingApproval}}
		code, output, stderr := runJSON(t, testApplication(t, engine), "status", "--json")
		want := []string{"latest:sylveste", "get:cycle-1"}
		if code != 0 || stderr != "" || output["id"] != "cycle-1" || !reflect.DeepEqual(engine.stateCalls, want) {
			t.Fatalf("code=%d output=%#v stderr=%q calls=%#v", code, output, stderr, engine.stateCalls)
		}
	})

	t.Run("doctor", func(t *testing.T) {
		engine := &fakeEngine{}
		code, output, stderr := runJSON(t, testApplication(t, engine), "doctor", "--json")
		if code != 0 || stderr != "" || output["healthy"] != true || !reflect.DeepEqual(engine.stateCalls, []string{"health"}) {
			t.Fatalf("code=%d output=%#v stderr=%q calls=%#v", code, output, stderr, engine.stateCalls)
		}
	})
}

func TestCLIExitCodesCoverUsageOperationalFailureAndCancellation(t *testing.T) {
	engine := &fakeEngine{}
	application := testApplication(t, engine)
	var stdout, stderr bytes.Buffer
	if code := application.Run(context.Background(), []string{"approve", "cycle-1", "--json"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("usage exit = %d", code)
	}

	engine.err = errors.New("intercore unavailable")
	stdout.Reset()
	stderr.Reset()
	if code := application.Run(context.Background(), []string{"status", "cycle-1", "--json"}, &stdout, &stderr); code != ExitFailure {
		t.Fatalf("failure exit = %d", code)
	}

	engine.err = context.DeadlineExceeded
	stdout.Reset()
	stderr.Reset()
	if code := application.Run(context.Background(), []string{"status", "cycle-1", "--json"}, &stdout, &stderr); code != ExitFailure {
		t.Fatalf("nested deadline exit = %d", code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout.Reset()
	stderr.Reset()
	if code := application.Run(ctx, []string{"cycle", "--json"}, &stdout, &stderr); code != ExitCanceled {
		t.Fatalf("canceled exit = %d", code)
	}
}

func TestCLIRejectsEmptyAndDuplicateOptionValuesBeforeDispatch(t *testing.T) {
	for name, args := range map[string][]string{
		"empty mode":      {"cycle", "--mode=", "--json"},
		"duplicate mode":  {"cycle", "--mode=shadow", "--mode=proposal", "--json"},
		"duplicate actor": {"approve", "cycle-1", "--actor=", "--actor=mk", "--json"},
	} {
		t.Run(name, func(t *testing.T) {
			engine := &fakeEngine{}
			code, _, _ := runJSON(t, testApplication(t, engine), args...)
			if code != ExitUsage || len(engine.serviceCalls) != 0 {
				t.Fatalf("code=%d calls=%#v", code, engine.serviceCalls)
			}
		})
	}
}

func TestDoctorReturnsStructuredFailedChecks(t *testing.T) {
	engine := &fakeEngine{err: errors.New("intercore unavailable")}
	application := testApplication(t, engine)
	var stdout, stderr bytes.Buffer
	code := application.Run(context.Background(), []string{"doctor", "--json"}, &stdout, &stderr)
	var report DoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report %q: %v", stdout.String(), err)
	}
	if code != ExitFailure || report.Healthy || !strings.Contains(stderr.String(), "unhealthy") {
		t.Fatalf("code=%d report=%#v stderr=%q", code, report, stderr.String())
	}
}

func TestCLISignsFailedStartAndPreservesOriginalError(t *testing.T) {
	engine := &fakeEngine{
		current:  domain.Cycle{ID: "cycle-failed", Stage: domain.StageFailed},
		startErr: errors.New("portfolio judgment failed"),
	}
	code, output, stderr := runJSON(t, testApplication(t, engine), "cycle", "--json")
	wantCalls := []string{"cycle", "compound:cycle-failed"}
	if code != ExitFailure || output["signed_receipt_id"] != "receipt-1" || !strings.Contains(stderr, "portfolio judgment failed") || !reflect.DeepEqual(engine.serviceCalls, wantCalls) || !engine.compoundHasDeadline {
		t.Fatalf("code=%d output=%#v stderr=%q calls=%#v", code, output, stderr, engine.serviceCalls)
	}
}

func TestCLIResumeSignsFailedExecutionAndPreservesOriginalError(t *testing.T) {
	engine := &fakeEngine{
		current:    domain.Cycle{ID: "cycle-failed", Stage: domain.StageApproved},
		executeErr: errors.New("executor failed"),
	}
	code, output, stderr := runJSON(t, testApplication(t, engine), "resume", "cycle-failed", "--json")
	wantCalls := []string{"execute:cycle-failed", "compound:cycle-failed"}
	if code != ExitFailure || output["signed_receipt_id"] != "receipt-1" || !strings.Contains(stderr, "executor failed") || !reflect.DeepEqual(engine.serviceCalls, wantCalls) {
		t.Fatalf("code=%d output=%#v stderr=%q calls=%#v", code, output, stderr, engine.serviceCalls)
	}
}

func TestCLIFailedCycleReportsOriginalAndSigningErrors(t *testing.T) {
	engine := &fakeEngine{
		current:     domain.Cycle{ID: "cycle-failed", Stage: domain.StageFailed},
		startErr:    errors.New("portfolio judgment failed"),
		compoundErr: errors.New("receipt emission failed"),
	}
	code, output, stderr := runJSON(t, testApplication(t, engine), "cycle", "--json")
	if code != ExitFailure || output["stage"] != string(domain.StageFailed) || !strings.Contains(stderr, "portfolio judgment failed") || !strings.Contains(stderr, "receipt emission failed") {
		t.Fatalf("code=%d output=%#v stderr=%q", code, output, stderr)
	}
}

func TestDoctorPropagatesCancellationExitCode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	engine := &fakeEngine{err: context.Canceled, onHealth: cancel}
	application := testApplication(t, engine)
	var stdout, stderr bytes.Buffer
	code := application.Run(ctx, []string{"doctor", "--json"}, &stdout, &stderr)
	if code != ExitCanceled || !strings.Contains(stderr.String(), context.Canceled.Error()) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
