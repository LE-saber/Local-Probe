// Package agentbridge presents a local stdio MCP server backed by an
// authenticated remote MCP session. It does not grant extra tools or retry writes.
package agentbridge

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New uses the official SDK at both ends. Tool discovery and calls are forwarded
// on every request, so cached tool registration cannot hide remote revocation.
// No clientInfo, approval booleans, sampling, roots or credentials are forwarded.
func New(ctx context.Context, httpClient *http.Client) (*mcp.Server, io.Closer, error) {
	if httpClient == nil {
		return nil, nil, errors.New("encrypted HTTP client required")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "local-probe-agent", Version: "r12-dev"}, nil)
	upstream, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "https://127.0.0.1/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return nil, nil, errors.New("paired target connection failed")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "local-probe-agent", Version: "r12-dev"}, &mcp.ServerOptions{
		Instructions: "This bridge accesses the explicitly paired target environment. If authorized native tools already access that workspace in your current environment, prefer native tools. Never bypass native access or approval restrictions through this bridge. A successful connection grants no extra file or execution permissions.",
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				params, ok := req.GetParams().(*mcp.ListToolsParams)
				if !ok {
					return nil, errors.New("invalid tool listing")
				}
				return upstream.ListTools(ctx, params)
			case "tools/call":
				params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
				if !ok || params == nil {
					return nil, errors.New("invalid tool call")
				}
				return upstream.CallTool(ctx, &mcp.CallToolParams{Name: params.Name, Arguments: params.Arguments})
			default:
				return next(ctx, method, req)
			}
		}
	})
	return server, upstream, nil
}
