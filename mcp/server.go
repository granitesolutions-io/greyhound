package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// JSON-RPC 2.0 types

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP protocol types

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type capabilities struct {
	Tools *toolsCap `json:"tools,omitempty"`
}

type toolsCap struct{}

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      serverInfo   `json:"serverInfo"`
	Capabilities    capabilities `json:"capabilities"`
}

type toolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema defines the JSON Schema for a tool's input parameters.
type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

// Property defines a single property within an InputSchema.
type Property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Items       *Items `json:"items,omitempty"`
}

// Items defines the element type for array properties.
type Items struct {
	Type string `json:"type"`
}

type toolsListResult struct {
	Tools []toolDefinition `json:"tools"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolHandler is a function that handles a tool call.
type ToolHandler func(args json.RawMessage) (string, error)

// tool represents a registered MCP tool.
type tool struct {
	Definition toolDefinition
	Handler    ToolHandler
}

// Server is a minimal MCP server using stdio JSON-RPC 2.0 transport.
type Server struct {
	name    string
	version string
	tools   map[string]tool
}

// NewServer creates a new MCP server with the given name and version.
func NewServer(name, version string) *Server {
	return &Server{
		name:    name,
		version: version,
		tools:   make(map[string]tool),
	}
}

// RegisterTool adds a tool to the server.
func (s *Server) RegisterTool(name, description string, schema InputSchema, handler ToolHandler) {
	s.tools[name] = tool{
		Definition: toolDefinition{
			Name:        name,
			Description: description,
			InputSchema: schema,
		},
		Handler: handler,
	}
}

// Run starts the stdio JSON-RPC loop (blocks until stdin closes).
func (s *Server) Run() error {
	fmt.Fprintln(os.Stderr, "[mcp] server started, waiting for input...")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		fmt.Fprintf(os.Stderr, "[mcp] recv: %s\n", line)

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			fmt.Fprintf(os.Stderr, "[mcp] parse error: %s\n", err)
			s.writeError(nil, -32700, "Parse error")
			continue
		}

		fmt.Fprintf(os.Stderr, "[mcp] method: %s\n", req.Method)
		s.handleRequest(req)
	}

	fmt.Fprintln(os.Stderr, "[mcp] stdin closed, shutting down")
	return scanner.Err()
}

func (s *Server) handleRequest(req jsonRPCRequest) {
	switch req.Method {
	case "initialize":
		s.writeResult(req.ID, initializeResult{
			ProtocolVersion: "2024-11-05",
			ServerInfo: serverInfo{
				Name:    s.name,
				Version: s.version,
			},
			Capabilities: capabilities{
				Tools: &toolsCap{},
			},
		})

	case "notifications/initialized":
		// No response needed for notifications

	case "tools/list":
		var defs []toolDefinition
		for _, t := range s.tools {
			defs = append(defs, t.Definition)
		}
		s.writeResult(req.ID, toolsListResult{Tools: defs})

	case "tools/call":
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.writeError(req.ID, -32602, "Invalid params")
			return
		}

		t, ok := s.tools[params.Name]
		if !ok {
			s.writeError(req.ID, -32601, fmt.Sprintf("Unknown tool: %s", params.Name))
			return
		}

		result, err := t.Handler(params.Arguments)
		if err != nil {
			s.writeResult(req.ID, toolResult{
				Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: %s", err)}},
				IsError: true,
			})
			return
		}

		s.writeResult(req.ID, toolResult{
			Content: []toolContent{{Type: "text", Text: result}},
		})

	default:
		if req.ID != nil {
			s.writeError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
		}
	}
}

func (s *Server) writeResult(id json.RawMessage, result interface{}) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintln(os.Stdout, string(data))
}

func (s *Server) writeError(id json.RawMessage, code int, message string) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintln(os.Stdout, string(data))
}
