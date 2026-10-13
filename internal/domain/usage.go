package domain

import "math"

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
	if !ValidCost(a) || !ValidCost(b) {
		return nil
	}
	v := *a + *b
	if math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// ValidCost reports whether c is a known, usable dollar cost: present, finite
// and not negative. An invalid cost is treated as unknown.
func ValidCost(c *float64) bool {
	return c != nil && *c >= 0 && !math.IsInf(*c, 0) && !math.IsNaN(*c)
}

// KnownCost returns c if it is a valid cost, otherwise nil (unknown).
func KnownCost(c *float64) *float64 {
	if !ValidCost(c) {
		return nil
	}
	return c
}

func sumInt(a, b *int64) *int64 {
	if a == nil || b == nil {
		return nil
	}
	if *a < 0 || *b < 0 || *a > math.MaxInt64-*b {
		return nil
	}
	v := *a + *b
	return &v
}

// Float and Int64 return a pointer to a reported value.
func Float(v float64) *float64 { return &v }
func Int64(v int64) *int64     { return &v }
