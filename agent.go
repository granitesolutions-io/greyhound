package greyhound

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

	// LogWriter receives raw log output. If nil, logging is discarded.
	LogWriter io.Writer

	// OnEvent is called for each streaming event from Claude CLI.
	// If nil, events are silently consumed.
	OnEvent func(Event)
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
}

// Run invokes the Claude CLI with the given prompt, streams events, and returns
// the accumulated text output.
func (a *Agent) Run(prompt string) (*Result, error) {
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

	cmd := exec.Command("claude", args...)
	cmd.Dir = a.WorkDir
	cmd.Stdin = strings.NewReader(prompt)

	// Clear CLAUDECODE env var to prevent nested session conflicts
	cmd.Env = filterEnv(os.Environ(), "CLAUDECODE")

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
	var sessionID string

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
			continue
		}

		switch event.Type {
		case "system":
			if event.Subtype == "init" {
				a.emit(Event{Type: EventInit})
			}

		case "assistant":
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
			if event.SessionID != "" {
				sessionID = event.SessionID
			}
			if event.Result.Text != "" {
				finalText.WriteString(event.Result.Text)
			}
			a.emit(Event{Type: EventResult, Text: event.Result.Text, SessionID: event.SessionID})

		case "user":
			a.emit(Event{Type: EventToolResult})
		}
	}

	cmdErr := cmd.Wait()

	if cmdErr != nil {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("claude exited with error: %w\nstderr: %s", cmdErr, stderrStr)
		}
		return nil, fmt.Errorf("claude exited with error: %w", cmdErr)
	}

	return &Result{
		Output:    finalText.String(),
		SessionID: sessionID,
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
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	SessionID string `json:"session_id,omitempty"`
	Result    struct {
		Text string `json:"text,omitempty"`
	} `json:"result"`
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
