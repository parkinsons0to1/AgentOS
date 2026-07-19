// Package capd is the capability broker: the only component that holds raw
// credentials. Sessions reach the network through its egress gateway, which
// checks the session's grants, injects credentials upstream-side, enforces
// request budgets, and writes every decision to the ledger.
package capd

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
	"github.com/parkinsons0to1/AgentOS/internal/toolbus"
)

// Gateway is the per-session egress proxy and tool router. It serves three
// surfaces on one loopback port:
//
//   - path mode:  GET /egress/<host>/<path...> — forwards to https://<host>
//     with the grant's credential injected; for agent-aware callers.
//   - proxy mode: standard HTTP proxy (CONNECT tunnels and absolute-URI
//     requests) so unmodified tools work via HTTP(S)_PROXY. CONNECT tunnels
//     are opaque TLS, so grants are enforced but credentials are NOT
//     injected there — credentialed hosts should use path mode.
//   - toolbus:    GET /tool, GET /tool/<server>, POST /tool/<server>/<tool>.
//
// The agent never sees a credential and cannot reach hosts outside its grants.
type Gateway struct {
	sess      *spec.Session
	secrets   *Secrets
	ledger    *ledger.Ledger
	bus       *toolbus.Bus
	client    *http.Client
	requests  atomic.Int64
	toolCalls atomic.Int64
	server    *http.Server
	addr      string
}

// NewGateway wires a gateway for one session. bus may be nil when the session
// declares no MCP servers.
func NewGateway(sess *spec.Session, secrets *Secrets, led *ledger.Ledger, bus *toolbus.Bus) *Gateway {
	return &Gateway{
		sess:    sess,
		secrets: secrets,
		ledger:  led,
		bus:     bus,
		client:  &http.Client{},
	}
}

// Start listens on a loopback port and serves until Shutdown. It returns the
// base URL sessions should use (exported as AGENTOS_GATEWAY and HTTP(S)_PROXY).
func (g *Gateway) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	g.addr = "http://" + ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/egress/", g.handleEgress)
	mux.HandleFunc("/status", g.handleStatus)
	mux.HandleFunc("/tool", g.handleToolIndex)
	mux.HandleFunc("/tool/", g.handleTool)
	g.server = &http.Server{Handler: proxyOrMux{g: g, mux: mux}}
	go g.server.Serve(ln)
	return g.addr, nil
}

// proxyOrMux routes proxy-shaped requests (CONNECT, absolute-URI) to the
// proxy handlers and everything else to the normal mux.
type proxyOrMux struct {
	g   *Gateway
	mux *http.ServeMux
}

func (h proxyOrMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		h.g.handleConnect(w, r)
	case r.URL.IsAbs():
		h.g.handleAbsoluteProxy(w, r)
	default:
		h.mux.ServeHTTP(w, r)
	}
}

// Shutdown stops the listener.
func (g *Gateway) Shutdown() {
	if g.server != nil {
		g.server.Close()
	}
}

// Addr returns the base URL after Start.
func (g *Gateway) Addr() string { return g.addr }

func (g *Gateway) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"session":             g.sess.Name,
		"egress_requests":     g.requests.Load(),
		"max_egress_requests": g.sess.Budgets.MaxEgressRequests,
		"tool_calls":          g.toolCalls.Load(),
		"max_tool_calls":      g.sess.Budgets.MaxToolCalls,
	})
}

// spendEgress consumes one unit of the egress budget. It returns false (and
// ledgers the violation) when the budget is exhausted.
func (g *Gateway) spendEgress(host string) bool {
	max := g.sess.Budgets.MaxEgressRequests
	if max > 0 && g.requests.Add(1) > int64(max) {
		g.requests.Add(-1)
		g.ledger.Emit("budget.exceeded", map[string]any{
			"budget": "max_egress_requests", "limit": max, "host": host,
		})
		return false
	}
	if max <= 0 {
		g.requests.Add(1)
	}
	return true
}

func (g *Gateway) handleEgress(w http.ResponseWriter, r *http.Request) {
	host, rest, ok := splitEgressPath(r.URL.Path)
	if !ok {
		g.deny(w, r, host, http.StatusBadRequest, "path must be /egress/<host>/<path>")
		return
	}

	grant := g.sess.GrantFor(host)
	if grant == nil {
		g.deny(w, r, host, http.StatusForbidden,
			fmt.Sprintf("egress to %q is not granted to this session", host))
		return
	}

	if !g.spendEgress(host) {
		g.deny(w, r, host, http.StatusTooManyRequests,
			fmt.Sprintf("egress budget exhausted (%d requests)", g.sess.Budgets.MaxEgressRequests))
		return
	}

	upstream := grant.Upstream
	if upstream == "" {
		upstream = "https://" + host
	}
	target, err := url.Parse(upstream)
	if err != nil {
		g.deny(w, r, host, http.StatusBadGateway, "bad upstream: "+err.Error())
		return
	}
	target.Path = "/" + rest
	target.RawQuery = r.URL.RawQuery

	g.forward(w, r, grant, host, target)
}

// handleAbsoluteProxy serves plain-HTTP proxy requests (GET http://host/…).
// Because the payload is visible here, grants behave exactly like path mode,
// including credential injection.
func (g *Gateway) handleAbsoluteProxy(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()
	grant := g.sess.GrantFor(host)
	if grant == nil {
		g.deny(w, r, host, http.StatusForbidden,
			fmt.Sprintf("egress to %q is not granted to this session", host))
		return
	}
	if !g.spendEgress(host) {
		g.deny(w, r, host, http.StatusTooManyRequests,
			fmt.Sprintf("egress budget exhausted (%d requests)", g.sess.Budgets.MaxEgressRequests))
		return
	}
	target := *r.URL
	if grant.Upstream != "" {
		up, err := url.Parse(grant.Upstream)
		if err != nil {
			g.deny(w, r, host, http.StatusBadGateway, "bad upstream: "+err.Error())
			return
		}
		target.Scheme, target.Host = up.Scheme, up.Host
	}
	g.forward(w, r, grant, host, &target)
}

// handleConnect serves HTTPS proxying for unmodified tools. The tunnel is
// opaque TLS: the grant is enforced on the CONNECT target and the tunnel is
// ledgered, but no credential can be injected into it.
func (g *Gateway) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if g.sess.GrantFor(host) == nil {
		g.deny(w, r, host, http.StatusForbidden,
			fmt.Sprintf("egress to %q is not granted to this session", host))
		return
	}
	if !g.spendEgress(host) {
		g.deny(w, r, host, http.StatusTooManyRequests,
			fmt.Sprintf("egress budget exhausted (%d requests)", g.sess.Budgets.MaxEgressRequests))
		return
	}

	upstream, err := net.DialTimeout("tcp", r.Host, 15*time.Second)
	if err != nil {
		g.deny(w, r, host, http.StatusBadGateway, err.Error())
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		g.deny(w, r, host, http.StatusInternalServerError, "hijacking unsupported")
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	g.ledger.Emit("egress.connect", map[string]any{"host": host, "target": r.Host})
	client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go func() {
		defer upstream.Close()
		defer client.Close()
		io.Copy(upstream, client)
	}()
	go func() {
		io.Copy(client, upstream)
	}()
}

// forward relays one visible-payload request upstream, injecting the grant's
// credential if any, and ledgers the outcome.
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, grant *spec.Grant, host string, target *url.URL) {
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		g.deny(w, r, host, http.StatusBadGateway, err.Error())
		return
	}
	copyHeaders(out.Header, r.Header)
	out.Host = target.Host

	injected := false
	if grant.Secret != "" {
		value, err := g.secrets.Get(grant.Secret)
		if err != nil {
			g.deny(w, r, host, http.StatusBadGateway, err.Error())
			return
		}
		format := grant.Inject.Format
		if format == "" {
			format = "{secret}"
		}
		out.Header.Set(grant.Inject.Header, strings.ReplaceAll(format, "{secret}", value))
		injected = true
	}

	resp, err := g.client.Do(out)
	if err != nil {
		g.deny(w, r, host, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	g.ledger.Emit("egress.request", map[string]any{
		"host": host, "method": r.Method, "path": target.Path,
		"status": resp.StatusCode, "secret_injected": injected,
	})

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// deny rejects a request with a structured error the agent can reason about,
// and ledgers the denial.
func (g *Gateway) deny(w http.ResponseWriter, r *http.Request, host string, code int, msg string) {
	g.ledger.Emit("egress.denied", map[string]any{
		"host": host, "method": r.Method, "path": r.URL.Path,
		"status": code, "reason": msg,
	})
	writeJSON(w, code, map[string]any{"error": msg, "host": host})
}

// handleToolIndex lists the session's declared MCP servers.
func (g *Gateway) handleToolIndex(w http.ResponseWriter, r *http.Request) {
	if g.bus == nil {
		writeJSON(w, http.StatusOK, map[string]any{"servers": []string{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": g.bus.Servers()})
}

// handleTool serves GET /tool/<server> (list tools, lazily starting the
// server) and POST /tool/<server>/<tool> (invoke with a JSON-args body).
func (g *Gateway) handleTool(w http.ResponseWriter, r *http.Request) {
	if g.bus == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no mcp servers declared in this session"})
		return
	}
	serverName, toolName, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/tool/"), "/")
	if serverName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "path must be /tool/<server>[/<tool>]"})
		return
	}

	switch {
	case r.Method == http.MethodGet && toolName == "":
		tools, err := g.bus.Tools(r.Context(), serverName)
		if err != nil {
			g.ledger.Emit("tool.denied", map[string]any{"server": serverName, "reason": err.Error()})
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"server": serverName, "tools": tools})

	case r.Method == http.MethodPost && toolName != "":
		if max := g.sess.Budgets.MaxToolCalls; max > 0 && g.toolCalls.Add(1) > int64(max) {
			g.toolCalls.Add(-1)
			g.ledger.Emit("budget.exceeded", map[string]any{
				"budget": "max_tool_calls", "limit": max, "server": serverName, "tool": toolName,
			})
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error": fmt.Sprintf("tool-call budget exhausted (%d calls)", max)})
			return
		}
		if g.sess.Budgets.MaxToolCalls <= 0 {
			g.toolCalls.Add(1)
		}
		args, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		result, err := g.bus.Call(r.Context(), serverName, toolName, args)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(result)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": "use GET /tool/<server> to list tools or POST /tool/<server>/<tool> to call one"})
	}
}

// splitEgressPath parses /egress/<host>/<rest> -> (host, rest).
func splitEgressPath(p string) (host, rest string, ok bool) {
	p = strings.TrimPrefix(p, "/egress/")
	host, rest, _ = strings.Cut(p, "/")
	if host == "" {
		return "", "", false
	}
	return host, rest, true
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		// Hop-by-hop and routing headers stay local.
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Proxy-Connection", "Keep-Alive", "Te", "Trailer",
			"Transfer-Encoding", "Upgrade", "Host":
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}
