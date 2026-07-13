package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mistakeknot/Remontoire/internal/app"
)

type fakeRunner struct {
	args []string
	code int
}

func (r *fakeRunner) Run(ctx context.Context, args []string, _, _ io.Writer) int {
	r.args = append([]string(nil), args...)
	if ctx.Err() != nil {
		return app.ExitCanceled
	}
	return r.code
}

func TestRunLoadsExplicitConfigAndPassesRemainingArguments(t *testing.T) {
	runner := &fakeRunner{}
	loaded := ""
	loader := func(path string) (commandRunner, error) {
		loaded = path
		return runner, nil
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"doctor", "--config=/tmp/remontoire.json", "--json"}, &stdout, &stderr, loader)
	if code != 0 || loaded != "/tmp/remontoire.json" || !reflect.DeepEqual(runner.args, []string{"doctor", "--json"}) || stderr.Len() != 0 {
		t.Fatalf("code=%d loaded=%q args=%#v stderr=%q", code, loaded, runner.args, stderr.String())
	}
}

func TestRunReportsConfigFailureAsStructuredUsageError(t *testing.T) {
	loader := func(string) (commandRunner, error) { return nil, errors.New("config is invalid") }
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"doctor", "--config=/tmp/bad.json", "--json"}, &stdout, &stderr, loader)
	var payload map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if code != app.ExitUsage || payload["error"] != "config is invalid" {
		t.Fatalf("code=%d payload=%#v", code, payload)
	}
}

func TestRunReportsConfigFlagParseFailureAsStructuredUsageError(t *testing.T) {
	called := false
	loader := func(string) (commandRunner, error) {
		called = true
		return &fakeRunner{}, nil
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"doctor", "--config=/tmp/one.json", "--config=/tmp/two.json", "--json"}, &stdout, &stderr, loader)
	var payload map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &payload); err != nil {
		t.Fatalf("decode stderr %q: %v", stderr.String(), err)
	}
	if code != app.ExitUsage || called || !strings.Contains(payload["error"].(string), "more than once") {
		t.Fatalf("code=%d called=%v payload=%#v", code, called, payload)
	}
}

func TestTakeConfigPathRejectsAnotherFlagAsItsValue(t *testing.T) {
	if _, _, err := takeConfigPath([]string{"doctor", "--config", "--json"}); err == nil {
		t.Fatal("expected missing config path error")
	}
	if _, _, err := takeConfigPath([]string{"doctor", "--config", ""}); err == nil {
		t.Fatal("expected empty config path error")
	}
	if _, _, err := takeConfigPath([]string{"doctor", "--config=", "--config=/tmp/remontoire.json"}); err == nil {
		t.Fatal("expected empty first config path error")
	}
}

func TestRunPropagatesCanceledSignalContext(t *testing.T) {
	runner := &fakeRunner{}
	loader := func(string) (commandRunner, error) { return runner, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code := run(ctx, []string{"cycle", "--config=/tmp/remontoire.json"}, io.Discard, io.Discard, loader)
	if code != app.ExitCanceled {
		t.Fatalf("exit = %d", code)
	}
}

func TestRestoreSignalHandlingAfterFirstCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	called := make(chan struct{})
	go restoreSignalHandling(ctx, func() { close(called) })
	cancel()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("signal handling was not restored")
	}
}
