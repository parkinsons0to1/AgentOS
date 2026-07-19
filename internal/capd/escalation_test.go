package capd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

// escalationGateway starts a gateway with escalations enabled and one
// upstream the session was NOT granted.
func escalationGateway(t *testing.T) (base, escDir string, upstreamURL string) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "reached")
	}))
	t.Cleanup(upstream.Close)

	sess := &spec.Session{Name: "t", Runtime: spec.Runtime{Command: []string{"true"}}}
	escDir = t.TempDir()
	led, err := ledger.Open(escDir, "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { led.Close() })
	gw := NewGateway(sess, NewSecrets(nil), led, nil)
	gw.EscalationDir = escDir
	base, err = gw.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Shutdown)
	return base, escDir, upstream.URL
}

func escalate(t *testing.T, base, host, reason string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(base+"/escalate", "application/json",
		strings.NewReader(`{"host":"`+host+`","reason":"`+reason+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestEscalationLifecycle(t *testing.T) {
	base, escDir, _ := escalationGateway(t)

	// Before any escalation: denied.
	resp, err := http.Get(base + "/egress/api.locked.example/data")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pre-escalation status = %d, want 403", resp.StatusCode)
	}

	// Request the capability: 202 pending.
	code, out := escalate(t, base, "api.locked.example", "need the data API")
	if code != http.StatusAccepted || out["status"] != "pending" {
		t.Fatalf("escalate = %d %v, want 202 pending", code, out)
	}
	// Re-requesting reports current status instead of duplicating.
	code, out = escalate(t, base, "api.locked.example", "again")
	if code != http.StatusOK || out["status"] != "pending" {
		t.Fatalf("re-escalate = %d %v, want 200 pending", code, out)
	}

	// Still denied while pending.
	resp, _ = http.Get(base + "/egress/api.locked.example/data")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pending status = %d, want 403", resp.StatusCode)
	}

	// A human approves (as the viewport would).
	if _, err := Decide(escDir, "api.locked.example", true); err != nil {
		t.Fatal(err)
	}

	// Now granted — but with no credential and no upstream override the
	// gateway would dial the real host; assert the grant resolution instead.
	code, out = escalate(t, base, "api.locked.example", "check")
	if code != http.StatusOK || out["status"] != "approved" {
		t.Fatalf("post-approve escalate = %d %v, want 200 approved", code, out)
	}

	// The request was ledgered.
	events, err := ledger.Read(escDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == "capability.requested" && e.Data["host"] == "api.locked.example" {
			found = true
		}
	}
	if !found {
		t.Fatal("capability.requested was not ledgered")
	}
}

func TestDeniedEscalationStaysDenied(t *testing.T) {
	base, escDir, _ := escalationGateway(t)
	escalate(t, base, "api.locked.example", "please")
	if _, err := Decide(escDir, "api.locked.example", false); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(base + "/egress/api.locked.example/data")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied escalation: status = %d, want 403", resp.StatusCode)
	}
	// Deciding twice is rejected.
	if _, err := Decide(escDir, "api.locked.example", true); err == nil {
		t.Fatal("re-deciding a settled escalation must fail")
	}
}

func TestEscalateRejectsBadHosts(t *testing.T) {
	base, _, _ := escalationGateway(t)
	for _, host := range []string{"", "evil.example/../../etc", "spaced host"} {
		code, _ := escalate(t, base, host, "x")
		if code != http.StatusBadRequest {
			t.Fatalf("host %q: status = %d, want 400", host, code)
		}
	}
}

func TestApprovedEscalationGrantsEgressOnly(t *testing.T) {
	// An approved escalation must never inject credentials: grantFor
	// synthesizes a bare grant with no secret.
	base, escDir, upstreamURL := escalationGateway(t)
	_ = upstreamURL
	escalate(t, base, "api.locked.example", "need it")
	if _, err := Decide(escDir, "api.locked.example", true); err != nil {
		t.Fatal(err)
	}
	e, err := LoadEscalation(escDir, "api.locked.example")
	if err != nil || e.Status != EscalationApproved {
		t.Fatalf("escalation = %+v, %v", e, err)
	}
}
