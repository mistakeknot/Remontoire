package domain

// Usage is measured spend for one backend call. A nil field is unknown: the
// backend did not report it. Unknown is never zero, and a zero pointer means
// the backend reported zero.
type Usage struct {
	CostUSD               *float64 `json:"cost_usd,omitempty"`
	InputTokens           *int64   `json:"input_tokens,omitempty"`
	CachedInputTokens     *int64   `json:"cached_input_tokens,omitempty"`
	OutputTokens          *int64   `json:"output_tokens,omitempty"`
	ReasoningOutputTokens *int64   `json:"reasoning_output_tokens,omitempty"`
}

// Combine adds two usages. A field is known only if both sides know it, so a
// missing value never turns into a smaller total.
func (u Usage) Combine(other Usage) Usage {
	return Usage{
		CostUSD:               sumFloat(u.CostUSD, other.CostUSD),
		InputTokens:           sumInt(u.InputTokens, other.InputTokens),
		CachedInputTokens:     sumInt(u.CachedInputTokens, other.CachedInputTokens),
		OutputTokens:          sumInt(u.OutputTokens, other.OutputTokens),
		ReasoningOutputTokens: sumInt(u.ReasoningOutputTokens, other.ReasoningOutputTokens),
	}
}

func sumFloat(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	v := *a + *b
	return &v
}

func sumInt(a, b *int64) *int64 {
	if a == nil || b == nil {
		return nil
	}
	v := *a + *b
	return &v
}

// Float and Int64 return a pointer to a reported value.
func Float(v float64) *float64 { return &v }
func Int64(v int64) *int64     { return &v }
