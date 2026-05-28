package ai

import "strings"

// Usage holds token usage from an AI model invocation.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Cost calculates the estimated cost in USD for the given model.
// Pricing is based on per-million-token rates from the Claude API.
func (u Usage) Cost(model string) float64 {
	p := modelPricing(model)
	return float64(u.InputTokens)*p.Input/1_000_000 +
		float64(u.OutputTokens)*p.Output/1_000_000 +
		float64(u.CacheCreationInputTokens)*p.CacheWrite/1_000_000 +
		float64(u.CacheReadInputTokens)*p.CacheRead/1_000_000
}

// Add returns a new Usage with the values from u and other summed.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:              u.InputTokens + other.InputTokens,
		OutputTokens:             u.OutputTokens + other.OutputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens + other.CacheCreationInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens + other.CacheReadInputTokens,
	}
}

// pricing holds per-million-token rates in USD.
type pricing struct {
	Input      float64
	Output     float64
	CacheWrite float64 // 1h cache write rate
	CacheRead  float64
}

// modelPricing returns pricing for a model name. Falls back to Sonnet pricing.
func modelPricing(model string) pricing {
	switch {
	case strings.Contains(model, "haiku"):
		return pricing{Input: 1, Output: 5, CacheWrite: 2, CacheRead: 0.10}
	case strings.Contains(model, "opus"):
		return pricing{Input: 5, Output: 25, CacheWrite: 10, CacheRead: 0.50}
	default: // sonnet and unknown models
		return pricing{Input: 3, Output: 15, CacheWrite: 6, CacheRead: 0.30}
	}
}
