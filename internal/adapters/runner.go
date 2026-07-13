package adapters

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

var ErrOutputLimit = errors.New("command output exceeds limit")

type Invocation struct {
	Name           string
	Args           []string
	Dir            string
	Stdin          []byte
	Env            []string
	MaxOutputBytes int
}

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type Runner interface {
	Run(context.Context, Invocation) (Result, error)
}

type CommandError struct {
	Name     string
	ExitCode int
	Stderr   string
	Cause    error
}

func (e *CommandError) Error() string {
	if e.ExitCode >= 0 {
		return fmt.Sprintf("%s exited %d: %s", e.Name, e.ExitCode, e.Stderr)
	}
	return fmt.Sprintf("%s failed: %v", e.Name, e.Cause)
}

func (e *CommandError) Unwrap() error { return e.Cause }

type ExecRunner struct {
	DefaultMaxOutputBytes int
}

func (r ExecRunner) Run(ctx context.Context, invocation Invocation) (Result, error) {
	limit := invocation.MaxOutputBytes
	if limit <= 0 {
		limit = r.DefaultMaxOutputBytes
	}
	if limit <= 0 {
		limit = 1 << 20
	}

	capture := newBoundedCapture(limit)
	cmd := exec.CommandContext(ctx, invocation.Name, invocation.Args...)
	cmd.Dir = invocation.Dir
	cmd.Stdin = bytes.NewReader(invocation.Stdin)
	cmd.Stdout = capture.stdoutWriter()
	cmd.Stderr = capture.stderrWriter()
	if invocation.Env != nil {
		cmd.Env = append([]string(nil), invocation.Env...)
	} else {
		cmd.Env = os.Environ()
	}

	err := cmd.Run()
	result := Result{Stdout: capture.stdoutBytes(), Stderr: capture.stderrBytes(), ExitCode: -1}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if capture.exceededLimit() {
		return result, ErrOutputLimit
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if err != nil {
		return result, &CommandError{
			Name:     invocation.Name,
			ExitCode: result.ExitCode,
			Stderr:   string(result.Stderr),
			Cause:    err,
		}
	}
	return result, nil
}

type boundedCapture struct {
	mu       sync.Mutex
	max      int
	written  int
	exceeded bool
	stdout   bytes.Buffer
	stderr   bytes.Buffer
}

func newBoundedCapture(max int) *boundedCapture {
	return &boundedCapture{max: max}
}

func (c *boundedCapture) stdoutWriter() io.Writer { return captureWriter{capture: c, stdout: true} }
func (c *boundedCapture) stderrWriter() io.Writer { return captureWriter{capture: c} }

func (c *boundedCapture) stdoutBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.stdout.Bytes()...)
}

func (c *boundedCapture) stderrBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.stderr.Bytes()...)
}

func (c *boundedCapture) exceededLimit() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exceeded
}

type captureWriter struct {
	capture *boundedCapture
	stdout  bool
}

func (w captureWriter) Write(p []byte) (int, error) {
	w.capture.mu.Lock()
	defer w.capture.mu.Unlock()
	remaining := w.capture.max - w.capture.written
	if remaining <= 0 {
		w.capture.exceeded = true
		return 0, ErrOutputLimit
	}
	toWrite := len(p)
	if toWrite > remaining {
		toWrite = remaining
		w.capture.exceeded = true
	}
	var err error
	if w.stdout {
		_, err = w.capture.stdout.Write(p[:toWrite])
	} else {
		_, err = w.capture.stderr.Write(p[:toWrite])
	}
	w.capture.written += toWrite
	if err != nil {
		return toWrite, err
	}
	if toWrite < len(p) {
		return toWrite, ErrOutputLimit
	}
	return toWrite, nil
}
