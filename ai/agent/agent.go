package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Agent invokes the Claude CLI to execute a prompt with optional MCP servers.
type Agent struct {
	// Name identifies the agent (used in log output).
	Name string

	// WorkDir is the working directory for the Claude CLI process.
	WorkDir string

	// MCPServers configures MCP servers to attach to the Claude session.
	MCPServers []MCPServer

	// SessionID, when set, resumes an existing Claude conversation via --resume.
	SessionID string

	// Model specifies the Claude model to use (e.g. "claude-sonnet-4-20250514").
	// If empty, the CLI default is used.
	Model string

	// MaxTurns limits the number of agentic tool-use turns. Zero means unlimited.
	MaxTurns int

	// Token is the Claude OAuth token for CLI authentication.
	// If set, it is passed as CLAUDE_CODE_OAUTH_TOKEN to the subprocess.
	Token string

	// History, when set, provides conversation history on demand.
	// It is only called when a stale session is detected and history
	// replay is needed — not on every request.
	History HistoryProvider

	// LogWriter receives raw log output. If nil, logging is discarded.
	LogWriter io.Writer

	// OnEvent is called for each streaming event from Claude CLI.
	// If nil, events are silently consumed.
	OnEvent func(Event)
}

// HistoryProvider loads conversation history on demand.
// It is only called when a stale session is detected and history replay is needed.
type HistoryProvider interface {
	LoadHistory() []HistoryMessage
}

// HistoryFunc is a function that implements HistoryProvider.
type HistoryFunc func() []HistoryMessage

// LoadHistory calls the function.
func (f HistoryFunc) LoadHistory() []HistoryMessage { return f() }

// HistoryMessage is a role/content pair for conversation history.
type HistoryMessage struct {
	Role    string
	Content string
}

// MCPServer describes an MCP server to attach to a Claude session.
type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// Result holds the output from a Claude CLI invocation.
type Result struct {
	// Output is the accumulated text output from Claude.
	Output string

	// SessionID is the Claude session identifier.
	SessionID string

	// Usage holds the accumulated token usage across all turns.
	Usage Usage

	// Model is the model that was used for the response.
	Model string

	// Cost is the estimated cost in USD for this invocation.
	Cost float64
}

// Run invokes the Claude CLI with the given prompt, streams events, and returns
// the accumulated text output. If SessionID is set but the session is stale
// (e.g. after a redeployment), Run automatically retries without --resume and
// prepends History to the prompt for context continuity.
func (a *Agent) Run(prompt string) (*Result, error) {
	result, err := a.run(prompt, a.SessionID)
	if err != nil && a.SessionID != "" && strings.Contains(err.Error(), "No conversation found") {
		// Stale session — retry without --resume, replaying history
		a.logf("Stale session %s, retrying with conversation history", a.SessionID)
		retryPrompt := prompt
		if a.History != nil {
			if messages := a.History.LoadHistory(); len(messages) > 0 {
				var history string
				for _, m := range messages {
					history += fmt.Sprintf("[%s]: %s\n\n", m.Role, m.Content)
				}
				retryPrompt = "Here is the conversation so far:\n\n" + history + "Now continue the conversation. " + prompt
			}
		}
		return a.run(retryPrompt, "")
	}
	return result, err
}

// run executes a single Claude CLI invocation with the given prompt and session ID.
func (a *Agent) run(prompt string, sessionID string) (*Result, error) {
	// Write MCP config if servers are configured
	var mcpConfigPath string
	if len(a.MCPServers) > 0 {
		path, err := writeMCPConfig(a.MCPServers, a.WorkDir)
		if err != nil {
			return nil, fmt.Errorf("writing MCP config: %w", err)
		}
		mcpConfigPath = path
		defer os.Remove(mcpConfigPath)
	}

	// Build command args
	args := []string{"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
	}
	if mcpConfigPath != "" {
		args = append(args, "--mcp-config", mcpConfigPath)
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if a.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", a.MaxTurns))
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = a.WorkDir
	cmd.Stdin = strings.NewReader(prompt)

	// Clear CLAUDECODE env var to prevent nested session conflicts
	cmd.Env = filterEnv(os.Environ(), "CLAUDECODE")

	// Set Claude OAuth token if provided
	if a.Token != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+a.Token)
	}

	var stderrBuf bytes.Buffer
	if a.LogWriter != nil {
		cmd.Stderr = io.MultiWriter(a.LogWriter, &stderrBuf)
	} else {
		cmd.Stderr = &stderrBuf
	}

	a.logf("Command: claude %s", strings.Join(args, " "))

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			return nil, fmt.Errorf("claude CLI not found — install it from https://claude.ai/code")
		}
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	a.logf("Claude started (PID %d)", cmd.Process.Pid)

	var finalText strings.Builder
	var nonJSON strings.Builder
	var resultSessionID string
	var totalUsage Usage
	var model string

	// Parse stream-json events
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Capture non-JSON output for error reporting
			nonJSON.WriteString(line)
			nonJSON.WriteString("\n")
			continue
		}

		// Capture session ID from any event that carries one
		if event.SessionID != "" {
			resultSessionID = event.SessionID
		}

		switch event.Type {
		case "system":
			if event.Subtype == "init" {
				a.emit(Event{Type: EventInit, SessionID: event.SessionID})
			}

		case "assistant":
			if event.Message.Model != "" {
				model = event.Message.Model
			}
			totalUsage = totalUsage.Add(event.Message.Usage)

			for _, content := range event.Message.Content {
				switch content.Type {
				case "text":
					text := strings.TrimSpace(content.Text)
					if text != "" {
						finalText.WriteString(content.Text)
						a.emit(Event{Type: EventText, Text: content.Text})
					}

				case "thinking":
					a.emit(Event{Type: EventThinking})

				case "tool_use":
					input, _ := json.Marshal(content.Input)
					a.emit(Event{
						Type:      EventToolUse,
						ToolName:  content.Name,
						ToolInput: string(input),
					})
				}
			}

		case "result":
			if event.IsError {
				nonJSON.WriteString(event.Result)
			} else if event.Result != "" {
				finalText.WriteString(event.Result)
			}
			a.emit(Event{Type: EventResult, Text: event.Result, SessionID: event.SessionID})

		case "user":
			for _, content := range event.Message.Content {
				if content.Type == "tool_result" {
					a.emit(Event{
						Type:       EventToolResult,
						ToolOutput: content.Content,
						ToolError:  content.IsError,
					})
				}
			}
		}
	}

	cmdErr := cmd.Wait()

	if cmdErr != nil {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		nonJSONStr := strings.TrimSpace(nonJSON.String())
		details := stderrStr
		if nonJSONStr != "" {
			if details != "" {
				details += "\n"
			}
			details += nonJSONStr
		}
		if details != "" {
			return nil, fmt.Errorf("claude exited with error: %w\n%s", cmdErr, details)
		}
		return nil, fmt.Errorf("claude exited with error: %w", cmdErr)
	}

	return &Result{
		Output:    finalText.String(),
		SessionID: resultSessionID,
		Usage:     totalUsage,
		Model:     model,
		Cost:      totalUsage.Cost(model),
	}, nil
}

// emit dispatches an event to the OnEvent callback if set.
func (a *Agent) emit(e Event) {
	if a.OnEvent != nil {
		a.OnEvent(e)
	}
}

// logf writes a formatted log line if LogWriter is set.
func (a *Agent) logf(format string, args ...interface{}) {
	if a.LogWriter != nil {
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(a.LogWriter, "[%s] %s\n", a.Name, msg)
	}
}

// streamEvent represents a parsed JSON event from claude stream-json output.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Model   string `json:"model"`
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Content string          `json:"content"`
			IsError bool            `json:"is_error"`
		} `json:"content"`
		Usage Usage `json:"usage"`
	} `json:"message"`
	SessionID string `json:"session_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Result    string `json:"result,omitempty"`
}

// writeMCPConfig creates a temporary MCP config file for the Claude CLI.
func writeMCPConfig(servers []MCPServer, dir string) (string, error) {
	mcpServers := make(map[string]interface{})
	for _, s := range servers {
		entry := map[string]interface{}{
			"command": s.Command,
			"args":    s.Args,
		}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		mcpServers[s.Name] = entry
	}

	config := map[string]interface{}{
		"mcpServers": mcpServers,
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}

	configPath := filepath.Join(dir, "mcp-config.json")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return "", err
	}

	return configPath, nil
}

// filterEnv returns a copy of env with any variable matching key removed.
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}
