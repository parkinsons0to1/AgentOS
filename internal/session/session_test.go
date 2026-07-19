package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parkinsons0to1/AgentOS/internal/capd"
	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

func TestRunHappyPathWritesLedgerAndManifest(t *testing.T) {
	state := t.TempDir()
	sess := &spec.Session{
		Name:    "happy",
		Runtime: spec.Runtime{Command: []string{"sh", "-c", `test -n "$AGENTOS_GATEWAY" && test -f "$AGENTOS_MANIFEST"`}},
		Egress:  []spec.Grant{{Host: "api.example.com"}},
	}
	res, err := Run(context.Background(), sess, capd.NewSecrets(nil), state)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0 (contract env vars missing?)", res.ExitCode)
	}

	events, err := ledger.Read(res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, e := range events {
		types[e.Type] = true
	}
	if !types["session.start"] || !types["session.end"] {
		t.Fatalf("ledger missing lifecycle events, got %v", types)
	}

	manifest, err := os.ReadFile(filepath.Join(res.Dir, "MANIFEST.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "api.example.com") {
		t.Fatal("manifest does not list the granted host")
	}
}

func TestRunEnforcesWallClockBudget(t *testing.T) {
	sess := &spec.Session{
		Name:    "slow",
		Runtime: spec.Runtime{Command: []string{"sleep", "10"}},
		Budgets: spec.Budgets{WallClockSeconds: 1},
	}
	start := time.Now()
	res, err := Run(context.Background(), sess, capd.NewSecrets(nil), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("session should have timed out")
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("timeout not enforced promptly, took %s", elapsed)
	}
}

func TestRunDoesNotLeakSupervisorEnv(t *testing.T) {
	t.Setenv("OPERATOR_SECRET_KEY", "must-not-leak")
	sess := &spec.Session{
		Name:    "envcheck",
		Runtime: spec.Runtime{Command: []string{"sh", "-c", `test -z "$OPERATOR_SECRET_KEY"`}},
	}
	res, err := Run(context.Background(), sess, capd.NewSecrets(nil), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatal("supervisor environment leaked into the session")
	}
}
