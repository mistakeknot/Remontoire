package adapters

import (
	"context"
	"reflect"
	"testing"
)

func TestOckhamWeights(t *testing.T) {
	runner := &recordingRunner{}
	runner.queue(`{"Revel-1":6,"Revel-2":-3}`)
	ockham := Ockham{Binary: "ockham", Dir: "/portfolio", Runner: runner}

	weights, degraded := ockham.Weights(context.Background())
	if degraded || weights["Revel-1"] != 6 || weights["Revel-2"] != -3 {
		t.Fatalf("weights = %#v degraded=%t", weights, degraded)
	}
	want := []string{"dispatch", "advise", "--json"}
	if got := runner.calls[0].Invocation.Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestOckhamAbsenceDegradesToNeutral(t *testing.T) {
	runner := &recordingRunner{}
	runner.queueExit(127, "not found")
	ockham := Ockham{Binary: "ockham", Runner: runner}
	weights, degraded := ockham.Weights(context.Background())
	if !degraded || len(weights) != 0 {
		t.Fatalf("weights = %#v degraded=%t", weights, degraded)
	}
}
