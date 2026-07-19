package capd

import (
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
	"github.com/parkinsons0to1/AgentOS/internal/toolbus"
)

func testToolGateway(t *testing.T, budgets spec.Budgets) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	sess := &spec.Session{
		Name:    "t",
		Runtime: spec.Runtime{Command: []string{"true"}},
		Budgets: budgets,
		MCPServers: []spec.MCPServer{
			{Name: "mock", Command: []string{"python3", "../toolbus/testdata/mock_mcp.py"}},
		},
	}
	led, err := ledger.Open(t.TempDir(), "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { led.Close() })
	bus := toolbus.New(sess.MCPServers, led)
	t.Cleanup(bus.Shutdown)
	gw := NewGateway(sess, NewSecrets(nil), led, bus)
	base, err := gw.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Shutdown)
	return base
}

func TestToolRoutesListAndCall(t *testing.T) {
	base := testToolGateway(t, spec.Budgets{})

	resp, err := http.Get(base + "/tool/mock")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"echo"`) {
		t.Fatalf("list: status=%d body=%s", resp.StatusCode, body)
	}

	resp, err = http.Post(base+"/tool/mock/echo", "application/json",
		strings.NewReader(`{"ping":"pong"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "pong") {
		t.Fatalf("call: status=%d body=%s", resp.StatusCode, body)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("call result is not MCP-shaped: %s", body)
	}
}

func TestToolRoutesEnforceCallBudget(t *testing.T) {
	base := testToolGateway(t, spec.Budgets{MaxToolCalls: 1})

	resp, err := http.Post(base+"/tool/mock/echo", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first call: status=%d, want 200", resp.StatusCode)
	}

	resp, err = http.Post(base+"/tool/mock/echo", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second call: status=%d, want 429", resp.StatusCode)
	}
}
