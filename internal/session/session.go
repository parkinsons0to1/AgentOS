// Package session runs one supervised agent session: it wires the ledger and
// egress gateway, writes the discoverability manifest, launches the runtime
// with a hardened environment, and enforces the wall-clock budget.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/parkinsons0to1/AgentOS/internal/apkg"
	"github.com/parkinsons0to1/AgentOS/internal/capd"
	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
	"github.com/parkinsons0to1/AgentOS/internal/toolbus"
)

// Result summarizes a finished session.
type Result struct {
	ExitCode  int
	TimedOut  bool
	Duration  time.Duration
	SessionID string
	Dir       string
}

// Run executes the session to completion. pkgRoot is the package store used
// to resolve sess.Packages; it may be empty when the spec references none.
func Run(ctx context.Context, sess *spec.Session, secrets *capd.Secrets, stateRoot, pkgRoot string) (*Result, error) {
	var binDirs []string
	var pkgs []*apkg.Package
	if len(sess.Packages) > 0 {
		var err error
		if pkgs, err = apkg.Resolve(pkgRoot, sess.Packages); err != nil {
			return nil, err
		}
		if binDirs, err = apkg.Apply(sess, pkgs); err != nil {
			return nil, err
		}
	}

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

	var bus *toolbus.Bus
	if len(sess.MCPServers) > 0 {
		bus = toolbus.New(sess.MCPServers, led)
		defer bus.Shutdown()
	}

	gw := capd.NewGateway(sess, secrets, led, bus)
	gw.EscalationDir = dir
	gwURL, err := gw.Start()
	if err != nil {
		return nil, err
	}
	defer gw.Shutdown()

	manifestPath := filepath.Join(dir, "MANIFEST.md")
	if err := os.WriteFile(manifestPath, []byte(manifest(sess, gwURL, pkgs)), 0o644); err != nil {
		return nil, err
	}

	led.Emit("session.start", map[string]any{
		"spec": sess.Name, "gateway": gwURL, "command": sess.Runtime.Command,
		"packages": sess.Packages, "grants": grantHosts(sess),
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
	cmd.Env = sessionEnv(sess, gwURL, manifestPath, binDirs)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.WaitDelay = 5 * time.Second

	start := time.Now()
	runErr := cmd.Start()
	if runErr == nil {
		writeMeta(dir, Meta{
			ID: id, Spec: sess.Name, PID: cmd.Process.Pid,
			StartedAt: start.UTC(), Command: sess.Runtime.Command,
		})
		runErr = cmd.Wait()
	}
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
	writeMeta(dir, Meta{
		ID: id, Spec: sess.Name, PID: 0, // 0 = no longer running
		StartedAt: start.UTC(), Command: sess.Runtime.Command,
		EndedAt: time.Now().UTC(), ExitCode: &res.ExitCode, TimedOut: res.TimedOut,
	})
	return res, nil
}

// Meta is the session's meta.json: enough for tooling (the dashboard) to
// show status and target the runtime process. PID 0 means finished.
type Meta struct {
	ID        string    `json:"id"`
	Spec      string    `json:"spec"`
	PID       int       `json:"pid"`
	Command   []string  `json:"command"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	TimedOut  bool      `json:"timed_out,omitempty"`
}

func writeMeta(dir string, m Meta) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(dir, "meta.json"), raw, 0o644)
}

// sessionEnv builds the runtime's environment. The parent environment is NOT
// inherited: leaking the supervisor's env (which may hold operator secrets)
// into the session would defeat the broker. Only spec-declared vars plus the
// AgentOS contract vars pass through, with PATH/HOME kept for usability.
func sessionEnv(sess *spec.Session, gwURL, manifestPath string, binDirs []string) []string {
	path := os.Getenv("PATH")
	if len(binDirs) > 0 {
		path = strings.Join(binDirs, ":") + ":" + path
	}
	env := []string{
		"AGENTOS=1",
		"AGENTOS_SESSION=" + sess.Name,
		"AGENTOS_GATEWAY=" + gwURL,
		"AGENTOS_MANIFEST=" + manifestPath,
		// Unmodified tools route through the gateway's proxy mode, so grants
		// apply to everything the session runs, not just gateway-aware code.
		"HTTP_PROXY=" + gwURL,
		"HTTPS_PROXY=" + gwURL,
		"http_proxy=" + gwURL,
		"https_proxy=" + gwURL,
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
		"PATH=" + path,
		"HOME=" + os.Getenv("HOME"),
	}
	for k, v := range sess.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func grantHosts(sess *spec.Session) []string {
	hosts := make([]string, 0, len(sess.Egress))
	for _, g := range sess.Egress {
		hosts = append(hosts, g.Host)
	}
	return hosts
}

// manifest renders the machine-readable "what can I do here" contract that
// every session finds at $AGENTOS_MANIFEST. Discoverability is a guarantee:
// the agent never has to guess its grants or budgets.
func manifest(sess *spec.Session, gwURL string, pkgs []*apkg.Package) string {
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
	b.WriteString("\n## Packages\n\n")
	if len(pkgs) == 0 {
		b.WriteString("None installed into this session.\n")
	} else {
		b.WriteString("Package CLIs are on PATH; their MCP servers and grants are listed below.\n\n")
		for _, p := range pkgs {
			fmt.Fprintf(&b, "- `%s` %s — %s\n", p.Name, p.Version, p.Description)
			for _, c := range p.Provides.CLIs {
				fmt.Fprintf(&b, "  - cli: `%s`\n", c)
			}
			for _, sk := range p.Provides.Skills {
				fmt.Fprintf(&b, "  - skill: `%s` (read it before related tasks)\n",
					filepath.Join(p.Dir, "skills", sk))
			}
		}
	}
	b.WriteString("\n## Tools (MCP)\n\n")
	if len(sess.MCPServers) == 0 {
		b.WriteString("No MCP servers declared.\n")
	} else {
		b.WriteString("List tools: `GET $AGENTOS_GATEWAY/tool/<server>` — ")
		b.WriteString("call one: `POST $AGENTOS_GATEWAY/tool/<server>/<tool>` with JSON arguments.\n\n")
		for _, m := range sess.MCPServers {
			fmt.Fprintf(&b, "- `%s`\n", m.Name)
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
	if sess.Budgets.MaxToolCalls > 0 {
		fmt.Fprintf(&b, "- tool calls: %d\n", sess.Budgets.MaxToolCalls)
	}
	if sess.Budgets.WallClockSeconds == 0 && sess.Budgets.MaxEgressRequests == 0 &&
		sess.Budgets.MaxToolCalls == 0 {
		b.WriteString("- none\n")
	}
	return b.String()
}
