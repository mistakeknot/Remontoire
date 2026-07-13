package adapters

import (
	"context"
	"encoding/json"
)

type Ockham struct {
	Binary string
	Dir    string
	Runner Runner
}

func (o Ockham) Weights(ctx context.Context) (map[string]int, bool) {
	if o.Runner == nil {
		return map[string]int{}, true
	}
	binary := o.Binary
	if binary == "" {
		binary = "ockham"
	}
	result, err := o.Runner.Run(ctx, Invocation{
		Name: binary,
		Args: []string{"dispatch", "advise", "--json"},
		Dir:  o.Dir,
	})
	if err != nil || result.ExitCode != 0 {
		return map[string]int{}, true
	}
	weights := map[string]int{}
	if err := json.Unmarshal(result.Stdout, &weights); err != nil {
		return map[string]int{}, true
	}
	return weights, false
}
