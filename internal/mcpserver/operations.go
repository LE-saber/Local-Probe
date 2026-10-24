package mcpserver

import (
	"context"
	"errors"

	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/LE-saber/Local-Probe/internal/operations"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var operationTools = []string{"probe_tools", "prepare_operation", "submit_operation", "operation_status", "operation_output", "cancel_operation", "prepare_file_change", "commit_file_change"}

func isOperationTool(name string) bool {
	for _, v := range operationTools {
		if name == v {
			return true
		}
	}
	return false
}
func (s *Server) operationSubject(ctx context.Context, b policy.BoundScope) (operations.Subject, bool) {
	if s.operations == nil || s.peerRegistry == nil {
		return operations.Subject{}, false
	}
	g, ok := deviceidentity.GrantFromContext(ctx)
	subject := operations.Subject{Principal: g.KeyPin, Connection: b.ConnectionID()}
	return subject, ok && g.ConnectionID == b.ConnectionID() && s.operations.Allows(subject)
}
func (s *Server) allowsTool(ctx context.Context, b policy.BoundScope, name string) bool {
	if !b.AllowsTool(name) {
		return false
	}
	if isOperationTool(name) {
		_, ok := s.operationSubject(ctx, b)
		return ok
	}
	return true
}
func (s *Server) addOperationTools(server *mcp.Server) {
	if s.operations == nil {
		return
	}
	properties := map[string]any{
		"nonce":   map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"kind":    map[string]any{"type": "string", "enum": []string{"command", "script", "build", "test", "version", "file_write", "file_patch"}},
		"tool_id": map[string]any{"type": "string"}, "root_id": map[string]any{"type": "string"}, "directory": map[string]any{"type": "string"},
		"args":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 64},
		"script": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
		"edits":           map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "object", "properties": map[string]any{"old": map[string]any{"type": "string"}, "new": map[string]any{"type": "string"}}, "required": []string{"old", "new"}, "additionalProperties": false}},
		"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 1800},
	}
	for _, name := range operationTools {
		name := name
		required := []string{"id"}
		props := map[string]any{"id": map[string]any{"type": "string"}, "digest": map[string]any{"type": "string"}}
		switch name {
		case "prepare_operation", "prepare_file_change":
			props = properties
			required = []string{"nonce", "root_id"}
		case "submit_operation", "commit_file_change":
			required = []string{"id", "digest"}
		case "operation_output":
			props = map[string]any{"id": map[string]any{"type": "string"}, "stream": map[string]any{"type": "string", "enum": []string{"stdout", "stderr"}}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 32768}}
			required = []string{"id", "stream"}
		case "probe_tools":
			props = map[string]any{}
			required = nil
		}
		schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		tool := &mcp.Tool{Name: name, Description: "Trusted-host operation. Each execution/file change requires separate approval on the target computer. Poll operation_status at least every 60 seconds while running; never automatically resubmit with a new nonce after a timeout.", InputSchema: schema}
		addTool(server, tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			b, err := s.boundScope(ctx)
			if err != nil {
				return deniedToolResult(), nil
			}
			subject, ok := s.operationSubject(ctx, b)
			if !ok {
				return deniedToolResult(), nil
			}
			var out any
			switch name {
			case "prepare_operation", "prepare_file_change":
				var input operations.Request
				if decodeArguments(req, &input) != nil {
					return operationError(operations.ErrInvalid), nil
				}
				if name == "prepare_file_change" {
					if input.Kind != "" && input.Kind != "file_write" && input.Kind != "file_patch" {
						return operationError(operations.ErrInvalid), nil
					}
					if input.Kind == "" {
						input.Kind = "file_write"
					}
				}
				out, err = s.operations.Prepare(ctx, subject, input)
			case "probe_tools":
				out = struct {
					Tools any    `json:"tools"`
					Note  string `json:"note"`
				}{s.operations.Tools(), "Local runner catalog verified at target startup; each execution rechecks identity and image hash. Obtain an actual version by preparing and locally approving a kind=version job. This call does not execute runners."}
			default:
				var input struct {
					ID     string `json:"id"`
					Digest string `json:"digest,omitempty"`
					Stream string `json:"stream,omitempty"`
					Offset int    `json:"offset,omitempty"`
					Limit  int    `json:"limit,omitempty"`
				}
				if decodeArguments(req, &input) != nil {
					return operationError(operations.ErrInvalid), nil
				}
				switch name {
				case "submit_operation", "commit_file_change":
					if name == "commit_file_change" {
						summary, statusErr := s.operations.Status(subject, input.ID)
						if statusErr != nil || summary.Kind != "file_write" && summary.Kind != "file_patch" {
							return operationError(operations.ErrInvalid), nil
						}
					}
					out, err = s.operations.Submit(subject, input.ID, input.Digest)
				case "operation_status":
					out, err = s.operations.Status(subject, input.ID)
				case "cancel_operation":
					out, err = s.operations.Cancel(ctx, subject, input.ID)
				case "operation_output":
					if input.Limit == 0 {
						input.Limit = 8192
					}
					out, err = s.operations.Output(subject, input.ID, input.Stream, input.Offset, input.Limit)
				}
			}
			if err != nil {
				return operationError(err), nil
			}
			return s.jsonResult(out), nil
		})
	}
}
func operationError(err error) *mcp.CallToolResult {
	code := "operation_unavailable"
	switch {
	case errors.Is(err, operations.ErrInvalid):
		code = "invalid_request"
	case errors.Is(err, operations.ErrDenied):
		code = "denied"
	case errors.Is(err, operations.ErrConflict):
		code = "operation_conflict"
	case errors.Is(err, operations.ErrStorage):
		code = "storage_unavailable"
	case errors.Is(err, operations.ErrCapacity):
		code = "capacity_reached"
	}
	return errorResult(code, "operation rejected; inspect the target's local control interface")
}
