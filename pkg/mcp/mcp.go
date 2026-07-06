// Package mcp serves orcinus over the Model Context Protocol (MCP) so any
// MCP-capable agent (Claude Desktop, Claude Code, Codex, opencode, Cursor, …)
// can use orcinus as live tools and read its skill recipes as resources.
//
// Transport: newline-delimited JSON-RPC 2.0 over stdio. Read-only by default;
// mutating tools require allowWrite.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/orcinustools/orcinus/pkg/deploy"
	"github.com/orcinustools/orcinus/pkg/skills"
	"github.com/orcinustools/orcinus/pkg/version"
)

const protocolVersion = "2024-11-05"

// Server is an MCP server exposing orcinus.
type Server struct {
	kubeconfig string
	allowWrite bool
	tools      []tool
}

type tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Write       bool
	Handle      func(s *Server, ctx context.Context, args map[string]any) (string, error)
}

// New returns an MCP server. allowWrite enables cluster-mutating tools.
func New(kubeconfig string, allowWrite bool) *Server {
	s := &Server{kubeconfig: kubeconfig, allowWrite: allowWrite}
	s.tools = builtinTools()
	return s
}

func (s *Server) applier() (*deploy.Applier, error) {
	cfg, err := deploy.LoadRESTConfig(s.kubeconfig)
	if err != nil {
		return nil, err
	}
	return deploy.NewApplier(cfg)
}

// --- JSON-RPC plumbing ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs the JSON-RPC loop until stdin closes.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024) // allow large compose bodies
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		result, rerr := s.dispatch(&req)
		if len(req.ID) == 0 {
			continue // notification: no response
		}
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = result
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) dispatch(req *rpcRequest) (any, *rpcError) {
	ctx := context.Background()
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
			"serverInfo":      map[string]any{"name": "orcinus", "version": version.Version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.toolDefs()}, nil
	case "tools/call":
		return s.callTool(ctx, req.Params)
	case "resources/list":
		return map[string]any{"resources": skillResources()}, nil
	case "resources/read":
		return readResource(req.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (s *Server) toolDefs() []map[string]any {
	var defs []map[string]any
	for _, t := range s.tools {
		if t.Write && !s.allowWrite {
			continue
		}
		desc := t.Description
		if t.Write {
			desc += " (mutates the cluster)"
		}
		defs = append(defs, map[string]any{
			"name": t.Name, "description": desc, "inputSchema": t.Schema,
		})
	}
	return defs
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	for _, t := range s.tools {
		if t.Name != p.Name {
			continue
		}
		if t.Write && !s.allowWrite {
			return toolResult("tool "+t.Name+" is disabled (start `orcinus mcp --allow-write` to enable cluster changes)", true), nil
		}
		text, err := t.Handle(s, ctx, p.Arguments)
		if err != nil {
			return toolResult("error: "+err.Error(), true), nil
		}
		return toolResult(text, false), nil
	}
	return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

// --- resources (skill cards) ---

func skillResources() []map[string]any {
	var out []map[string]any
	for _, c := range skills.List() {
		out = append(out, map[string]any{
			"uri": "orcinus://skills/" + c.Name, "name": c.Name,
			"description": c.Description, "mimeType": "text/markdown",
		})
	}
	return out
}

func readResource(params json.RawMessage) (any, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	name := strings.TrimPrefix(p.URI, "orcinus://skills/")
	c, ok := skills.Get(name)
	if !ok {
		return nil, &rpcError{Code: -32602, Message: "unknown resource: " + p.URI}
	}
	return map[string]any{"contents": []map[string]any{{
		"uri": p.URI, "mimeType": "text/markdown",
		"text": "# " + c.Name + " — " + c.Description + "\n\n" + c.Body,
	}}}, nil
}

// --- helpers for args ---

func argStr(a map[string]any, k string) string {
	if v, ok := a[k].(string); ok {
		return v
	}
	return ""
}

func argBool(a map[string]any, k string) bool {
	v, _ := a[k].(bool)
	return v
}

func argInt(a map[string]any, k string) (int, bool) {
	switch v := a[k].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

func toJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
