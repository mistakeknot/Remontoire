package harness

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/adapters"
)

// These are offline environment fixtures, never IDs sent to a BB server.
func poolContextFixture(t *testing.T) {
	t.Helper()
	original := environ
	t.Cleanup(func() { environ = original })
	environ = func() []string {
		return []string{
			"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(),
			"BB_THREAD_ID=thr_fixture", "BB_PROJECT_ID=proj_fixture",
			"BB_ENVIRONMENT_ID=env_fixture", "BB_CLI=/opt/bb/bin/bb",
			"BB_SERVER_URL=http://127.0.0.1:38886", "BB_HOST_DAEMON_PORT=38888",
			"BB_POOL_TOKEN=secret", "BB_UNRELATED=untrusted", "OPENAI_API_KEY=secret",
			"PWD=/untrusted-shell-directory",
		}
	}
}

func envValue(env []string, key string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, key+"="); ok {
			return value
		}
	}
	return ""
}

func TestCodexPoolPreservesCallerContextOnlyForAdmission(t *testing.T) {
	poolContextFixture(t)
	runner := &fakeRunner{}
	_, transport, err := (Codex{Runner: runner, PoolInputDir: t.TempDir()}).run(context.Background(), []string{"exec", "--cd=/model-workspace"}, []byte("prompt"))
	if err != nil || transport != CodexTransportPooled || len(runner.calls) != 1 {
		t.Fatalf("run = %s, %v; calls=%d", transport, err, len(runner.calls))
	}
	call := runner.calls[0]
	for _, key := range []string{"BB_THREAD_ID", "BB_PROJECT_ID", "BB_ENVIRONMENT_ID", "BB_CLI", "BB_SERVER_URL", "BB_HOST_DAEMON_PORT"} {
		if envValue(call.Env, key) == "" {
			t.Errorf("pool invocation lost inherited %s", key)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if call.Dir != cwd {
		t.Errorf("pool cwd = %q, want actual caller cwd %q", call.Dir, cwd)
	}
	for _, key := range []string{"BB_POOL_TOKEN", "BB_UNRELATED", "OPENAI_API_KEY", "PWD"} {
		if envValue(call.Env, key) != "" {
			t.Errorf("pool environment leaked %s", key)
		}
	}
	plain, cleanup, err := SafeEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, entry := range plain {
		if strings.HasPrefix(entry, "BB_") {
			t.Fatalf("benchmark/direct environment received BB context: %s", entry)
		}
	}
}

func TestCodexContextDenialRemainsTerminal(t *testing.T) {
	for _, state := range []string{"missing", "stale", "wrong-host", "destroyed-environment"} {
		t.Run(state, func(t *testing.T) {
			poolContextFixture(t)
			if state == "missing" {
				environ = func() []string { return []string{"PATH=/usr/bin:/bin"} }
			}
			runner := &fakeRunner{
				responses: []adapters.Result{{ExitCode: 1, Stderr: []byte("Pool exec requires a live thread on the primary host and its caller cwd, with a timeout of 1–3600 seconds.\n")}},
				errors:    []error{errors.New("pool admission denied")},
			}
			_, transport, err := (Codex{Runner: runner, PoolInputDir: t.TempDir()}).run(context.Background(), []string{"exec"}, []byte("prompt"))
			if err == nil || transport != CodexTransportPoolUnconfirmed || len(runner.calls) != 1 || runner.calls[0].Name != "bb" {
				t.Fatalf("denial replayed: transport=%q err=%v calls=%v", transport, err, runner.calls)
			}
			if state == "missing" && envValue(runner.calls[0].Env, "BB_THREAD_ID") != "" {
				t.Fatal("manufactured thread context")
			}
		})
	}
}

func TestCodexDirectFallbackDoesNotInheritBBContext(t *testing.T) {
	poolContextFixture(t)
	runner := &fakeRunner{responses: []adapters.Result{{ExitCode: 1, Stderr: []byte("Account Pooler cannot currently serve codex\n")}, {}}}
	_, _, _ = (Codex{Runner: runner, PoolInputDir: t.TempDir()}).run(context.Background(), []string{"exec"}, []byte("prompt"))
	if len(runner.calls) != 2 {
		t.Fatalf("expected existing pre-start fallback, calls=%d", len(runner.calls))
	}
	for _, entry := range runner.calls[1].Env {
		if strings.HasPrefix(entry, "BB_") {
			t.Fatalf("direct child inherited BB context: %s", entry)
		}
	}
}
