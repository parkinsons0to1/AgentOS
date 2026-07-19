package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRegistry is an in-memory OCI registry v2 implementation. With
// requireAuth it issues Bearer challenges and runs a token endpoint,
// mimicking ghcr.io / registry:2-with-auth.
type fakeRegistry struct {
	mu          sync.Mutex
	blobs       map[string][]byte
	manifests   map[string][]byte
	uploads     map[string][]byte
	requireAuth bool
	user, pass  string
	tokenURL    string
	nextUpload  int
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		blobs: map[string][]byte{}, manifests: map[string][]byte{}, uploads: map[string][]byte{},
	}
}

func (f *fakeRegistry) authorized(r *http.Request) bool {
	if !f.requireAuth {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer valid-registry-token"
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.URL.Path == "/token" {
		user, pass, ok := r.BasicAuth()
		if !ok || user != f.user || pass != f.pass {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "valid-registry-token"})
		return
	}

	if !f.authorized(r) {
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer realm="%s",service="fake"`, f.tokenURL))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	path := r.URL.Path
	switch {
	case strings.Contains(path, "/blobs/uploads/") && r.Method == http.MethodPost:
		f.nextUpload++
		id := fmt.Sprintf("upload-%d", f.nextUpload)
		f.uploads[id] = nil
		repo := strings.TrimSuffix(strings.TrimPrefix(path, "/v2/"), "/blobs/uploads/")
		w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/"+id)
		w.WriteHeader(http.StatusAccepted)

	case strings.Contains(path, "/blobs/uploads/") && r.Method == http.MethodPut:
		digest := r.URL.Query().Get("digest")
		body, _ := io.ReadAll(r.Body)
		if digestOf(body) != digest {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		}
		f.blobs[digest] = body
		w.WriteHeader(http.StatusCreated)

	case strings.Contains(path, "/blobs/") && r.Method == http.MethodHead:
		digest := path[strings.LastIndex(path, "/")+1:]
		if _, ok := f.blobs[digest]; ok {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}

	case strings.Contains(path, "/blobs/") && r.Method == http.MethodGet:
		digest := path[strings.LastIndex(path, "/")+1:]
		blob, ok := f.blobs[digest]
		if !ok {
			http.Error(w, "no such blob", http.StatusNotFound)
			return
		}
		w.Write(blob)

	case strings.Contains(path, "/manifests/") && r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.manifests[path] = body
		w.WriteHeader(http.StatusCreated)

	case strings.Contains(path, "/manifests/") && r.Method == http.MethodGet:
		m, ok := f.manifests[path]
		if !ok {
			http.Error(w, "no such manifest", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", manifestMediaType)
		w.Write(m)

	default:
		http.Error(w, "unhandled: "+r.Method+" "+path, http.StatusNotFound)
	}
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "apkg.yaml"),
		[]byte("name: demo\nversion: 1.0.0\nprovides:\n  clis: [tool]\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bin", "tool"),
		[]byte("#!/bin/sh\necho tool\n"), 0o755)
	return dir
}

func clientFor(srv *httptest.Server) (*Client, Ref) {
	host := strings.TrimPrefix(srv.URL, "http://")
	return &Client{Scheme: "http", HTTP: srv.Client()},
		Ref{Registry: host, Repository: "owner/demo", Tag: "1.0.0"}
}

func TestParseRef(t *testing.T) {
	ref, err := ParseRef("ghcr.io/owner/agentos-pkg/github:0.1.0")
	if err != nil || ref.Registry != "ghcr.io" ||
		ref.Repository != "owner/agentos-pkg/github" || ref.Tag != "0.1.0" {
		t.Fatalf("ref = %+v, err = %v", ref, err)
	}
	ref, err = ParseRef("localhost:5000/demo")
	if err != nil || ref.Tag != "latest" || ref.Registry != "localhost:5000" {
		t.Fatalf("default tag: %+v, %v", ref, err)
	}
	if _, err := ParseRef("no-registry-host"); err == nil {
		t.Fatal("bare name must be rejected")
	}
}

func TestPushPullRoundTrip(t *testing.T) {
	reg := newFakeRegistry()
	srv := httptest.NewServer(reg)
	defer srv.Close()
	client, ref := clientFor(srv)

	digest, err := client.Push(ref, fixtureDir(t), map[string]string{"org.agentos.pkg": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest = %q", digest)
	}

	dest := t.TempDir()
	if err := client.Pull(ref, dest); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dest, "apkg.yaml"))
	if err != nil || !strings.Contains(string(raw), "name: demo") {
		t.Fatalf("manifest after pull: %s, %v", raw, err)
	}
	info, err := os.Stat(filepath.Join(dest, "bin", "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatal("executable bit lost through push/pull")
	}
}

func TestPushIdempotentBlobs(t *testing.T) {
	reg := newFakeRegistry()
	srv := httptest.NewServer(reg)
	defer srv.Close()
	client, ref := clientFor(srv)
	dir := fixtureDir(t)
	if _, err := client.Push(ref, dir, nil); err != nil {
		t.Fatal(err)
	}
	// Second push must succeed via HEAD-skip (no duplicate uploads needed).
	if _, err := client.Push(ref, dir, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTokenDance(t *testing.T) {
	reg := newFakeRegistry()
	reg.requireAuth = true
	reg.user, reg.pass = "ada", "pat-123"
	srv := httptest.NewServer(reg)
	defer srv.Close()
	reg.tokenURL = srv.URL + "/token"

	client, ref := clientFor(srv)
	client.Username, client.Password = "ada", "pat-123"
	if _, err := client.Push(ref, fixtureDir(t), nil); err != nil {
		t.Fatalf("authenticated push failed: %v", err)
	}
	if err := client.Pull(ref, t.TempDir()); err != nil {
		t.Fatalf("authenticated pull failed: %v", err)
	}

	bad, _ := clientFor(srv)
	bad.Username, bad.Password = "ada", "wrong"
	if _, err := bad.Push(ref, fixtureDir(t), nil); err == nil {
		t.Fatal("push with wrong credentials must fail")
	}
}

func TestPullRejectsTraversal(t *testing.T) {
	if err := untarGz(evilTar(t), t.TempDir()); err == nil {
		t.Fatal("tar with .. path must be rejected")
	}
}

// evilTar crafts an archive whose entry escapes the destination.
func evilTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("owned")
	tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
