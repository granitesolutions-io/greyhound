package agent

import "strings"

// EventType identifies the kind of streaming event from Claude CLI.
type EventType string

const (
	// EventInit is emitted when the Claude session initializes (system/init).
	EventInit EventType = "init"

	// EventText is emitted for assistant text content.
	EventText EventType = "text"

	// EventThinking is emitted for extended thinking blocks.
	EventThinking EventType = "thinking"

	// EventToolUse is emitted when the assistant invokes a tool.
	EventToolUse EventType = "tool_use"

	// EventToolResult is emitted when a tool result is received.
	EventToolResult EventType = "tool_result"

	// EventResult is emitted when the final result is received.
	EventResult EventType = "result"
)

// Event represents a single streaming event from the Claude CLI.
type Event struct {
	Type       EventType
	Text       string
	ToolName   string
	ToolInput  string
	ToolOutput string
	ToolError  bool
	SessionID  string
}

// Usage holds token usage from a Claude CLI invocation.
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

// pricing holds per-million-token rates in USD.
type pricing struct {
	Input      float64
	Output     float64
	CacheWrite float64 // 1h cache write rate
	CacheRead  float64
}

// modelPricing returns pricing for a model name. Falls back to Sonnet pricing.
func modelPricing(model string) pricing {
	// Map model IDs to pricing. Claude CLI reports full model IDs like
	// "claude-sonnet-4-20250514" or short names like "claude-opus-4-6".
	switch {
	case strings.Contains(model, "haiku"):
		return pricing{Input: 1, Output: 5, CacheWrite: 2, CacheRead: 0.10}
	case strings.Contains(model, "opus"):
		return pricing{Input: 5, Output: 25, CacheWrite: 10, CacheRead: 0.50}
	default: // sonnet and unknown models
		return pricing{Input: 3, Output: 15, CacheWrite: 6, CacheRead: 0.30}
	}
}
