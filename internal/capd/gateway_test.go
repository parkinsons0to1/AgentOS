package capd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

// testGateway starts a gateway for sess with its ledger in a fresh temp dir.
// It returns the gateway base URL and the ledger dir for reading events back.
func testGateway(t *testing.T, sess *spec.Session, secrets *Secrets) (base, dir string) {
	t.Helper()
	dir = t.TempDir()
	led, err := ledger.Open(dir, sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { led.Close() })
	gw := NewGateway(sess, secrets, led)
	base, err = gw.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Shutdown)
	return base, dir
}

func TestGatewayInjectsSecretForGrantedHost(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	sess := &spec.Session{
		Name:    "t",
		Runtime: spec.Runtime{Command: []string{"true"}},
		Egress: []spec.Grant{{
			Host:     "api.example.com",
			Secret:   "example-token",
			Inject:   &spec.Inject{Header: "Authorization", Format: "Bearer {secret}"},
			Upstream: upstream.URL,
		}},
	}
	secrets := NewSecrets(map[string]string{"example-token": "s3cr3t"})
	base, _ := testGateway(t, sess, secrets)

	resp, err := http.Get(base + "/egress/api.example.com/v1/things")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Fatalf("upstream Authorization = %q, want Bearer s3cr3t", gotAuth)
	}
}

func TestGatewayDeniesUngrantedHostAndLedgersIt(t *testing.T) {
	sess := &spec.Session{Name: "t", Runtime: spec.Runtime{Command: []string{"true"}}}
	base, dir := testGateway(t, sess, NewSecrets(nil))

	resp, err := http.Get(base + "/egress/evil.example.com/exfil")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	events, err := ledger.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == "egress.denied" && e.Data["host"] == "evil.example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("denial was not ledgered; events: %+v", events)
	}
}

func TestGatewayEnforcesRequestBudget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	sess := &spec.Session{
		Name:    "t",
		Runtime: spec.Runtime{Command: []string{"true"}},
		Budgets: spec.Budgets{MaxEgressRequests: 2},
		Egress:  []spec.Grant{{Host: "api.example.com", Upstream: upstream.URL}},
	}
	base, _ := testGateway(t, sess, NewSecrets(nil))

	for i := 0; i < 2; i++ {
		resp, err := http.Get(base + "/egress/api.example.com/ping")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, resp.StatusCode)
		}
	}

	resp, err := http.Get(base + "/egress/api.example.com/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over-budget status = %d, want 429", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "budget") {
		t.Fatalf("over-budget body should mention budget, got %s", body)
	}
}

func TestSecretsRejectLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/secrets.yaml"
	if err := os.WriteFile(path, []byte("token: abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecrets(path); err == nil {
		t.Fatal("LoadSecrets accepted a world-readable file; want error")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Get("token")
	if err != nil || v != "abc" {
		t.Fatalf("Get(token) = %q, %v", v, err)
	}
}
