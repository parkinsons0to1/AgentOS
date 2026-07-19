package toolbus

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

func newTestBus(t *testing.T) *Bus {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	led, err := ledger.Open(t.TempDir(), "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { led.Close() })
	b := New([]spec.MCPServer{
		{Name: "mock", Command: []string{"python3", "testdata/mock_mcp.py"}},
	}, led)
	t.Cleanup(b.Shutdown)
	return b
}

func TestToolsListsLazilyStartedServer(t *testing.T) {
	b := newTestBus(t)
	tools, err := b.Tools(context.Background(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want [echo]", tools)
	}
}

func TestCallRoundTripsArguments(t *testing.T) {
	b := newTestBus(t)
	res, err := b.Call(context.Background(), "mock", "echo",
		json.RawMessage(`{"greeting":"hello agent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), "hello agent") {
		t.Fatalf("result does not echo arguments: %s", res)
	}
}

func TestCallSurfacesServerErrors(t *testing.T) {
	b := newTestBus(t)
	_, err := b.Call(context.Background(), "mock", "no-such-tool", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("err = %v, want rpc 'unknown tool' error", err)
	}
}

func TestUndeclaredServerIsRejected(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Call(context.Background(), "ghost", "echo", nil); err == nil {
		t.Fatal("calling an undeclared server must fail")
	}
}
