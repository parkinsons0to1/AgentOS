// Package toolbus is the session's tool router: MCP servers declared in the
// session spec are spawned lazily on first use, spoken to over stdio
// (newline-delimited JSON-RPC), and every call is ledgered. Agents reach the
// bus through the gateway's /tool HTTP surface — no SDK required, curl works.
package toolbus

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

const (
	protocolVersion = "2024-11-05"
	initTimeout     = 30 * time.Second
)

// ToolInfo describes one tool exposed by an MCP server.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// Bus routes tool calls to the session's declared MCP servers.
type Bus struct {
	led     *ledger.Ledger
	mu      sync.Mutex
	servers map[string]*server
	order   []string
}

// New builds a bus for the declared servers. Nothing is spawned yet.
func New(specs []spec.MCPServer, led *ledger.Ledger) *Bus {
	b := &Bus{led: led, servers: map[string]*server{}}
	for _, s := range specs {
		b.servers[s.Name] = &server{spec: s}
		b.order = append(b.order, s.Name)
	}
	return b
}

// Servers returns the declared server names in spec order.
func (b *Bus) Servers() []string { return b.order }

// Tools lists the tools of one server, starting it if needed.
func (b *Bus) Tools(ctx context.Context, name string) ([]ToolInfo, error) {
	s, err := b.server(name)
	if err != nil {
		return nil, err
	}
	raw, err := s.rpc(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: bad tools/list response: %w", name, err)
	}
	return out.Tools, nil
}

// Call invokes one tool and returns the raw MCP result.
func (b *Bus) Call(ctx context.Context, name, tool string, args json.RawMessage) (json.RawMessage, error) {
	s, err := b.server(name)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	res, err := s.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	b.led.Emit("tool.call", map[string]any{
		"server": name, "tool": tool, "ok": err == nil,
	})
	return res, err
}

// Shutdown stops all spawned servers.
func (b *Bus) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.servers {
		s.stop()
	}
}

func (b *Bus) server(name string) (*server, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.servers[name]
	if !ok {
		return nil, fmt.Errorf("mcp server %q is not declared in this session", name)
	}
	return s, nil
}

// server is one MCP server process. Calls are serialized per server; the
// protocol over a single stdio pipe is effectively synchronous anyway.
type server struct {
	spec    spec.MCPServer
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   *json.Encoder
	stdout  *bufio.Reader
	nextID  int64
	started bool
}

type rpcResponse struct {
	ID     *int64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

func (s *server) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureStartedLocked(); err != nil {
		return nil, err
	}
	return s.roundTripLocked(ctx, method, params)
}

func (s *server) ensureStartedLocked() error {
	if s.started {
		return nil
	}
	cmd := exec.Command(s.spec.Command[0], s.spec.Command[1:]...)
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	for k, v := range s.spec.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting mcp server %s: %w", s.spec.Name, err)
	}
	s.cmd = cmd
	s.stdin = json.NewEncoder(stdin)
	s.stdout = bufio.NewReader(stdout)
	s.started = true

	initCtx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	if _, err := s.roundTripLocked(initCtx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "agentos-toolbus", "version": "0.1"},
	}); err != nil {
		s.stopLocked()
		return fmt.Errorf("initializing mcp server %s: %w", s.spec.Name, err)
	}
	// Fire-and-forget per spec; servers must not reply to notifications.
	s.stdin.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return nil
}

func (s *server) roundTripLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.nextID++
	id := s.nextID
	if err := s.stdin.Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		s.stopLocked()
		return nil, fmt.Errorf("%s: writing %s: %w", s.spec.Name, method, err)
	}

	type outcome struct {
		raw json.RawMessage
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		for {
			line, err := s.stdout.ReadBytes('\n')
			if err != nil {
				done <- outcome{nil, fmt.Errorf("%s: reading response: %w", s.spec.Name, err)}
				return
			}
			var resp rpcResponse
			if err := json.Unmarshal(line, &resp); err != nil || resp.ID == nil || *resp.ID != id {
				continue // server-initiated notifications/requests are skipped in v0
			}
			if resp.Error != nil {
				done <- outcome{nil, resp.Error}
				return
			}
			done <- outcome{resp.Result, nil}
			return
		}
	}()

	select {
	case o := <-done:
		return o.raw, o.err
	case <-ctx.Done():
		// The pipe is now desynchronized; restart the server on next use.
		s.stopLocked()
		return nil, fmt.Errorf("%s: %s: %w", s.spec.Name, method, ctx.Err())
	}
}

func (s *server) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *server) stopLocked() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	s.cmd = nil
	s.started = false
}
