package mcpserver

import (
	"context"
	"net/http"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type FoundationInfoInput struct{}

type FoundationInfoOutput struct {
	Build  buildinfo.Info `json:"build"`
	Status string         `json:"status"`
}

func foundationInfo(
	context.Context,
	*mcp.CallToolRequest,
	FoundationInfoInput,
) (*mcp.CallToolResult, FoundationInfoOutput, error) {
	return nil, FoundationInfoOutput{
		Build:  buildinfo.Current(),
		Status: "foundation_ready",
	}, nil
}

func New() *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "lctk", Version: buildinfo.Version},
		nil,
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        "foundation_info",
			Description: "Return LCTK foundation build and readiness information.",
		},
		foundationInfo,
	)
	return server
}

func NewHTTPHandler() *mcp.StreamableHTTPHandler {
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			return New()
		},
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
		},
	)
}
