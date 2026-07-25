package mcpserver

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStreamableHTTPFoundationInfo(t *testing.T) {
	httpServer := httptest.NewServer(NewHTTPHandler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(
		&mcp.Implementation{Name: "lctk-test", Version: buildinfo.Version},
		nil,
	)
	session, err := client.Connect(
		ctx,
		&mcp.StreamableClientTransport{Endpoint: httpServer.URL},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "foundation_info",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool returned an MCP error: %#v", result)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["status"] != "foundation_ready" {
		t.Fatalf("unexpected structured content: %#v", result.StructuredContent)
	}
}
