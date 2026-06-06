package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/uMatheusx/mcp-gateway/internal/config"
	"github.com/uMatheusx/mcp-gateway/internal/gateway"
)

// StdioServer reads JSON-RPC requests from stdin and writes responses to stdout,
// implementing the MCP stdio transport.
type StdioServer struct {
	cfg     *config.GatewayConfig
	gateway *gateway.Handler
	in      io.Reader
	out     io.Writer
}

// NewStdioServer creates a StdioServer wired to os.Stdin / os.Stdout.
func NewStdioServer(cfg *config.GatewayConfig, gw *gateway.Handler) *StdioServer {
	return &StdioServer{
		cfg:     cfg,
		gateway: gw,
		in:      os.Stdin,
		out:     os.Stdout,
	}
}

// Serve reads from s.in line-by-line until EOF or ctx is cancelled.
// Each non-empty line is parsed as a JSON-RPC 2.0 request. Notifications
// (requests without an id) are silently accepted and discarded.
func (s *StdioServer) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024) // 4 MB per message

	enc := json.NewEncoder(s.out)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			enc.Encode(ErrResponse(nil, ErrParse, "parse error: "+err.Error())) //nolint:errcheck
			continue
		}

		// Notifications have no ID; skip them without responding.
		if req.ID == nil {
			continue
		}

		resp := s.dispatch(ctx, &req)
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("writing response: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	return nil
}

func (s *StdioServer) dispatch(ctx context.Context, req *Request) Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return ErrResponse(req.ID, ErrMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *StdioServer) handleInitialize(req *Request) Response {
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    ServerCapabilities{Tools: &ToolsCapability{}},
		ServerInfo: ServerInfo{
			Name:    s.cfg.Name,
			Version: s.cfg.Version,
		},
	}
	return OKResponse(req.ID, result)
}

func (s *StdioServer) handleToolsList(req *Request) Response {
	tools := make([]Tool, 0, len(s.cfg.Tools))
	for name, t := range s.cfg.Tools {
		tools = append(tools, buildToolDef(name, t))
	}
	return OKResponse(req.ID, ToolsListResult{Tools: tools})
}

func (s *StdioServer) handleToolsCall(ctx context.Context, req *Request) Response {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return ErrResponse(req.ID, ErrInvalidParams, "invalid params: "+err.Error())
	}

	result, err := s.gateway.CallTool(ctx, params.Name, params.Arguments)
	if err != nil {
		return OKResponse(req.ID, ToolCallResult{
			Content: []Content{{Type: "text", Text: "error: " + err.Error()}},
			IsError: true,
		})
	}

	return OKResponse(req.ID, ToolCallResult{
		Content: []Content{{Type: "text", Text: result}},
	})
}

func buildToolDef(name string, t config.ToolConfig) Tool {
	schema := InputSchema{
		Type:       "object",
		Properties: make(map[string]PropertySchema),
	}

	for _, p := range t.Parameters {
		schema.Properties[p.Name] = PropertySchema{
			Type:        p.Type,
			Description: p.Description,
			Enum:        p.Enum,
			Minimum:     p.Minimum,
			Maximum:     p.Maximum,
		}
		if p.Required {
			schema.Required = append(schema.Required, p.Name)
		}
	}

	if t.Body != nil {
		for _, prop := range t.Body.Properties {
			schema.Properties[prop.Name] = PropertySchema{
				Type:        prop.Type,
				Description: prop.Description,
			}
			if prop.Required {
				schema.Required = append(schema.Required, prop.Name)
			}
		}
	}

	return Tool{
		Name:        name,
		Description: t.Description,
		InputSchema: schema,
	}
}
