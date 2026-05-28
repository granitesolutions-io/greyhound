package agent

import "github.com/granitesolutions-io/greyhound/ai"

// Usage is an alias for ai.Usage for backward compatibility.
type Usage = ai.Usage

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
