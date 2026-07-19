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

	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

// Gateway is the per-session egress proxy. The agent inside the session calls
//
//	GET http://127.0.0.1:<port>/egress/<host>/<path...>
//
// and the gateway forwards to https://<host>/<path...> iff the session spec
// grants that host, attaching the grant's credential. The agent never sees
// the credential and cannot reach hosts outside its grants.
type Gateway struct {
	sess     *spec.Session
	secrets  *Secrets
	ledger   *ledger.Ledger
	client   *http.Client
	requests atomic.Int64
	server   *http.Server
	addr     string
}

// NewGateway wires a gateway for one session.
func NewGateway(sess *spec.Session, secrets *Secrets, led *ledger.Ledger) *Gateway {
	return &Gateway{
		sess:    sess,
		secrets: secrets,
		ledger:  led,
		client:  &http.Client{},
	}
}

// Start listens on a loopback port and serves until Shutdown. It returns the
// base URL sessions should use (exported as AGENTOS_GATEWAY).
func (g *Gateway) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	g.addr = "http://" + ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/egress/", g.handleEgress)
	mux.HandleFunc("/status", g.handleStatus)
	g.server = &http.Server{Handler: mux}
	go g.server.Serve(ln)
	return g.addr, nil
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
	used := g.requests.Load()
	writeJSON(w, http.StatusOK, map[string]any{
		"session":             g.sess.Name,
		"egress_requests":     used,
		"max_egress_requests": g.sess.Budgets.MaxEgressRequests,
	})
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

	if max := g.sess.Budgets.MaxEgressRequests; max > 0 {
		if g.requests.Add(1) > int64(max) {
			g.requests.Add(-1)
			g.ledger.Emit("budget.exceeded", map[string]any{
				"budget": "max_egress_requests", "limit": max, "host": host,
			})
			g.deny(w, r, host, http.StatusTooManyRequests,
				fmt.Sprintf("egress budget exhausted (%d requests)", max))
			return
		}
	} else {
		g.requests.Add(1)
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
		"host": host, "method": r.Method, "path": "/" + rest,
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
