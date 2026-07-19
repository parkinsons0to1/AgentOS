package dash

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/session"
)

func writeSession(t *testing.T, root, id string, meta session.Meta, events []string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, "meta.json"), raw, 0o644)
	led, err := ledger.Open(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close()
	for _, ev := range events {
		led.Emit(ev, map[string]any{"n": 1})
	}
}

func TestListSessionsWithStatus(t *testing.T) {
	root := t.TempDir()
	exit := 0
	writeSession(t, root, "done-1", session.Meta{
		ID: "done-1", PID: 0, ExitCode: &exit,
		StartedAt: time.Now().Add(-time.Hour),
	}, []string{"session.start", "session.end"})
	writeSession(t, root, "live-1", session.Meta{
		ID: "live-1", PID: os.Getpid(), // the test process itself: always alive
		StartedAt: time.Now(),
	}, []string{"session.start"})

	srv := httptest.NewServer(Handler(root))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Sessions []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(out.Sessions))
	}
	// Newest first.
	if out.Sessions[0].ID != "live-1" || out.Sessions[0].Status != "running" {
		t.Fatalf("first = %+v, want live-1/running", out.Sessions[0])
	}
	if out.Sessions[1].Status != "finished" {
		t.Fatalf("second = %+v, want finished", out.Sessions[1])
	}
}

func TestLedgerTailWithOffset(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "s1", session.Meta{ID: "s1", StartedAt: time.Now()},
		[]string{"session.start", "egress.request", "session.end"})

	srv := httptest.NewServer(Handler(root))
	defer srv.Close()

	get := func(offset string) (events []ledger.Event, next int) {
		resp, err := http.Get(srv.URL + "/api/sessions/s1/ledger?offset=" + offset)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Events []ledger.Event `json:"events"`
			Next   int            `json:"next"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Events, out.Next
	}

	events, next := get("0")
	if len(events) != 3 || next != 3 {
		t.Fatalf("full read: %d events next=%d, want 3/3", len(events), next)
	}
	events, next = get("2")
	if len(events) != 1 || events[0].Type != "session.end" || next != 3 {
		t.Fatalf("offset read: %+v next=%d", events, next)
	}
}

func TestKillSwitchTerminatesRunningSession(t *testing.T) {
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Process.Kill()

	root := t.TempDir()
	writeSession(t, root, "victim", session.Meta{
		ID: "victim", PID: sleeper.Process.Pid, StartedAt: time.Now(),
	}, []string{"session.start"})

	srv := httptest.NewServer(Handler(root))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/sessions/victim/kill", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("kill: status=%d, want 200", resp.StatusCode)
	}
	done := make(chan error, 1)
	go func() { done <- sleeper.Wait() }()
	select {
	case <-done: // SIGTERM landed
	case <-time.After(5 * time.Second):
		t.Fatal("process still alive after kill endpoint")
	}
}

func TestKillRejectsTraversalAndFinished(t *testing.T) {
	root := t.TempDir()
	exit := 0
	writeSession(t, root, "done", session.Meta{ID: "done", PID: 0, ExitCode: &exit,
		StartedAt: time.Now()}, []string{"session.end"})

	srv := httptest.NewServer(Handler(root))
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/api/sessions/done/kill", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("finished kill: status=%d, want 409", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/sessions/..%2f..%2fetc/kill", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("path traversal must not reach a kill")
	}
}

func TestIndexServesHTML(t *testing.T) {
	srv := httptest.NewServer(Handler(t.TempDir()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index: status=%d type=%s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
