package adapters

import (
	"context"
	"fmt"
)

type recordedCall struct {
	Invocation Invocation
}

type queuedResponse struct {
	Result Result
	Err    error
}

type recordingRunner struct {
	calls     []recordedCall
	responses []queuedResponse
}

func (r *recordingRunner) Run(_ context.Context, invocation Invocation) (Result, error) {
	r.calls = append(r.calls, recordedCall{Invocation: invocation})
	if len(r.responses) == 0 {
		return Result{}, nil
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.Result, response.Err
}

func (r *recordingRunner) queue(stdout string) {
	r.responses = append(r.responses, queuedResponse{Result: Result{Stdout: []byte(stdout)}})
}

func (r *recordingRunner) queueExit(code int, stderr string) {
	r.responses = append(r.responses, queuedResponse{Result: Result{ExitCode: code, Stderr: []byte(stderr)}, Err: fmt.Errorf("exit %d", code)})
}
