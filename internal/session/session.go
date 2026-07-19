// Package session runs one supervised agent session: it wires the ledger and
// egress gateway, writes the discoverability manifest, launches the runtime
// with a hardened environment, and enforces the wall-clock budget.
package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/parkinsons0to1/AgentOS/internal/capd"
	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

// Result summarizes a finished session.
type Result struct {
	ExitCode  int
	TimedOut  bool
	Duration  time.Duration
	SessionID string
	Dir       string
}

// Run executes the session to completion.
func Run(ctx context.Context, sess *spec.Session, secrets *capd.Secrets, stateRoot string) (*Result, error) {
	id := fmt.Sprintf("%s-%d", sess.Name, time.Now().Unix())
	dir := filepath.Join(stateRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	led, err := ledger.Open(dir, id)
	if err != nil {
		return nil, err
	}
	defer led.Close()

	gw := capd.NewGateway(sess, secrets, led)
	gwURL, err := gw.Start()
	if err != nil {
		return nil, err
	}
	defer gw.Shutdown()

	manifestPath := filepath.Join(dir, "MANIFEST.md")
	if err := os.WriteFile(manifestPath, []byte(manifest(sess, gwURL)), 0o644); err != nil {
		return nil, err
	}

	led.Emit("session.start", map[string]any{
		"spec": sess.Name, "gateway": gwURL, "command": sess.Runtime.Command,
	})

	runCtx := ctx
	var cancel context.CancelFunc
	if sess.Budgets.WallClockSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx,
			time.Duration(sess.Budgets.WallClockSeconds)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, sess.Runtime.Command[0], sess.Runtime.Command[1:]...)
	if sess.Workspace != "" {
		if err := os.MkdirAll(sess.Workspace, 0o755); err != nil {
			return nil, err
		}
		cmd.Dir = sess.Workspace
	}
	cmd.Env = sessionEnv(sess, gwURL, manifestPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.WaitDelay = 5 * time.Second

	start := time.Now()
	runErr := cmd.Run()
	res := &Result{
		Duration:  time.Since(start),
		SessionID: id,
		Dir:       dir,
		TimedOut:  runCtx.Err() == context.DeadlineExceeded,
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if runErr != nil && cmd.ProcessState == nil {
		led.Emit("session.error", map[string]any{"error": runErr.Error()})
		return nil, runErr
	}

	if res.TimedOut {
		led.Emit("budget.exceeded", map[string]any{
			"budget": "wall_clock_seconds", "limit": sess.Budgets.WallClockSeconds,
		})
	}
	led.Emit("session.end", map[string]any{
		"exit_code": res.ExitCode, "timed_out": res.TimedOut,
		"duration_ms": res.Duration.Milliseconds(),
	})
	return res, nil
}

// sessionEnv builds the runtime's environment. The parent environment is NOT
// inherited: leaking the supervisor's env (which may hold operator secrets)
// into the session would defeat the broker. Only spec-declared vars plus the
// AgentOS contract vars pass through, with PATH/HOME kept for usability.
func sessionEnv(sess *spec.Session, gwURL, manifestPath string) []string {
	env := []string{
		"AGENTOS=1",
		"AGENTOS_SESSION=" + sess.Name,
		"AGENTOS_GATEWAY=" + gwURL,
		"AGENTOS_MANIFEST=" + manifestPath,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	for k, v := range sess.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// manifest renders the machine-readable "what can I do here" contract that
// every session finds at $AGENTOS_MANIFEST. Discoverability is a guarantee:
// the agent never has to guess its grants or budgets.
func manifest(sess *spec.Session, gwURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# AgentOS Session Manifest\n\n")
	fmt.Fprintf(&b, "- session: `%s`\n- egress gateway: `%s`\n\n", sess.Name, gwURL)
	b.WriteString("## Network access\n\n")
	if len(sess.Egress) == 0 {
		b.WriteString("No egress granted. All outbound requests will be denied.\n")
	} else {
		b.WriteString("Reach an allowed host via `$AGENTOS_GATEWAY/egress/<host>/<path>`.\n")
		b.WriteString("Credentials are attached by the gateway; do not send your own.\n\n")
		for _, g := range sess.Egress {
			auth := "no credential"
			if g.Secret != "" {
				auth = "credential injected"
			}
			fmt.Fprintf(&b, "- `%s` (%s)\n", g.Host, auth)
		}
	}
	b.WriteString("\n## Budgets\n\n")
	if sess.Budgets.WallClockSeconds > 0 {
		fmt.Fprintf(&b, "- wall clock: %ds\n", sess.Budgets.WallClockSeconds)
	}
	if sess.Budgets.MaxEgressRequests > 0 {
		fmt.Fprintf(&b, "- egress requests: %d (check `$AGENTOS_GATEWAY/status`)\n",
			sess.Budgets.MaxEgressRequests)
	}
	if sess.Budgets.WallClockSeconds == 0 && sess.Budgets.MaxEgressRequests == 0 {
		b.WriteString("- none\n")
	}
	return b.String()
}
