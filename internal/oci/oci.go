// Package oci pushes and pulls apkg packages as OCI artifacts, so packages
// distribute over any standard container registry (ghcr.io, Docker Hub,
// self-hosted registry:2) with no new transport. A package is one gzipped
// tarball layer plus a small config blob, tied together by an OCI manifest.
package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	manifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	configMediaType   = "application/vnd.agentos.apkg.config.v1+json"
	layerMediaType    = "application/vnd.agentos.apkg.layer.v1.tar+gzip"
)

// Ref is a parsed registry reference: host[/path...]:tag.
type Ref struct {
	Registry   string // e.g. ghcr.io
	Repository string // e.g. owner/agentos-pkg/github
	Tag        string // e.g. 0.1.0 or latest
}

// ParseRef splits "ghcr.io/owner/name:tag" (tag defaults to latest).
func ParseRef(s string) (Ref, error) {
	host, rest, ok := strings.Cut(s, "/")
	if !ok || !strings.ContainsAny(host, ".:") {
		return Ref{}, fmt.Errorf("ref %q must look like registry.host/repo/name[:tag]", s)
	}
	repo, tag := rest, "latest"
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		repo, tag = rest[:i], rest[i+1:]
	}
	if repo == "" || tag == "" {
		return Ref{}, fmt.Errorf("ref %q has an empty repository or tag", s)
	}
	return Ref{Registry: host, Repository: repo, Tag: tag}, nil
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        descriptor        `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Client talks to one registry. Credentials are optional: anonymous works
// for public pulls; pushes typically need Username/Password (for ghcr.io a
// GitHub username + PAT) or a static Token.
type Client struct {
	// Scheme defaults to https; tests override with http.
	Scheme   string
	Username string
	Password string
	Token    string // static bearer token; skips the challenge dance
	HTTP     *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) scheme() string {
	if c.Scheme != "" {
		return c.Scheme
	}
	return "https"
}

// Push uploads dir as an OCI artifact at ref. Returns the manifest digest.
func (c *Client) Push(ref Ref, dir string, annotations map[string]string) (string, error) {
	layer, err := tarGz(dir)
	if err != nil {
		return "", err
	}
	config, _ := json.Marshal(map[string]any{"kind": "agentos-apkg"})

	layerDesc := describe(layerMediaType, layer)
	configDesc := describe(configMediaType, config)
	if err := c.uploadBlob(ref, layerDesc.Digest, layer); err != nil {
		return "", fmt.Errorf("uploading layer: %w", err)
	}
	if err := c.uploadBlob(ref, configDesc.Digest, config); err != nil {
		return "", fmt.Errorf("uploading config: %w", err)
	}

	m := manifest{
		SchemaVersion: 2, MediaType: manifestMediaType,
		Config: configDesc, Layers: []descriptor{layerDesc},
		Annotations: annotations,
	}
	raw, _ := json.Marshal(m)
	url := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", c.scheme(), ref.Registry, ref.Repository, ref.Tag)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", manifestMediaType)
	resp, err := c.do(req, ref)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", registryError("putting manifest", resp)
	}
	return digestOf(raw), nil
}

// Pull downloads the artifact at ref and unpacks its layer into destDir.
func (c *Client) Pull(ref Ref, destDir string) error {
	url := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", c.scheme(), ref.Registry, ref.Repository, ref.Tag)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", manifestMediaType)
	resp, err := c.do(req, ref)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return registryError("getting manifest", resp)
	}
	var m manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&m); err != nil {
		return fmt.Errorf("decoding manifest: %w", err)
	}
	if len(m.Layers) != 1 || m.Layers[0].MediaType != layerMediaType {
		return fmt.Errorf("ref is not an agentos package (layers: %+v)", m.Layers)
	}

	blobURL := fmt.Sprintf("%s://%s/v2/%s/blobs/%s", c.scheme(), ref.Registry, ref.Repository, m.Layers[0].Digest)
	req, _ = http.NewRequest(http.MethodGet, blobURL, nil)
	resp2, err := c.do(req, ref)
	if err != nil {
		return err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return registryError("getting layer", resp2)
	}
	blob, err := io.ReadAll(io.LimitReader(resp2.Body, 256<<20))
	if err != nil {
		return err
	}
	if got := digestOf(blob); got != m.Layers[0].Digest {
		return fmt.Errorf("layer digest mismatch: manifest says %s, got %s", m.Layers[0].Digest, got)
	}
	return untarGz(blob, destDir)
}

func (c *Client) uploadBlob(ref Ref, digest string, blob []byte) error {
	// Skip if the registry already has it.
	headURL := fmt.Sprintf("%s://%s/v2/%s/blobs/%s", c.scheme(), ref.Registry, ref.Repository, digest)
	req, _ := http.NewRequest(http.MethodHead, headURL, nil)
	if resp, err := c.do(req, ref); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
	}

	startURL := fmt.Sprintf("%s://%s/v2/%s/blobs/uploads/", c.scheme(), ref.Registry, ref.Repository)
	req, _ = http.NewRequest(http.MethodPost, startURL, nil)
	resp, err := c.do(req, ref)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return registryError("starting upload", resp)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return fmt.Errorf("registry sent no upload location")
	}
	if strings.HasPrefix(loc, "/") {
		loc = fmt.Sprintf("%s://%s%s", c.scheme(), ref.Registry, loc)
	}
	sep := "?"
	if strings.Contains(loc, "?") {
		sep = "&"
	}
	req, _ = http.NewRequest(http.MethodPut, loc+sep+"digest="+digest, bytes.NewReader(blob))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err = c.do(req, ref)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return registryError("completing upload", resp)
	}
	return nil
}

// do sends a request, handling the registry token dance: on a 401 with a
// Bearer challenge it fetches a token from the auth realm (using basic
// credentials if configured) and retries once.
func (c *Client) do(req *http.Request, ref Ref) (*http.Response, error) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	var bodyCopy []byte
	if req.Body != nil {
		var err error
		if bodyCopy, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyCopy))
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || c.Token != "" {
		return resp, nil
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	token, err := c.fetchToken(challenge, ref)
	if err != nil {
		return nil, err
	}
	retry := req.Clone(req.Context())
	if bodyCopy != nil {
		retry.Body = io.NopCloser(bytes.NewReader(bodyCopy))
	}
	retry.Header.Set("Authorization", "Bearer "+token)
	return c.httpClient().Do(retry)
}

// fetchToken implements the Bearer challenge exchange used by ghcr.io,
// Docker Hub, and registry:2 with auth.
func (c *Client) fetchToken(challenge string, ref Ref) (string, error) {
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "", fmt.Errorf("registry requires auth (challenge: %q); set credentials", challenge)
	}
	params := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(challenge, "Bearer "), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			params[k] = strings.Trim(v, `"`)
		}
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("bearer challenge without realm: %q", challenge)
	}
	tokenURL := realm + "?service=" + params["service"]
	if params["scope"] != "" {
		tokenURL += "&scope=" + params["scope"]
	} else {
		tokenURL += "&scope=repository:" + ref.Repository + ":pull,push"
	}
	req, _ := http.NewRequest(http.MethodGet, tokenURL, nil)
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange failed: %s", resp.Status)
	}
	var out struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token != "" {
		return out.Token, nil
	}
	return out.AccessToken, nil
}

func describe(mediaType string, blob []byte) descriptor {
	return descriptor{MediaType: mediaType, Digest: digestOf(blob), Size: int64(len(blob))}
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func registryError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("%s: %s: %s", op, resp.Status, strings.TrimSpace(string(body)))
}

// tarGz packs dir into a deterministic gzipped tarball (sorted paths,
// zeroed timestamps, mode preserved so CLIs stay executable).
func tarGz(dir string) ([]byte, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, path := range paths {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		hdr := &tar.Header{
			Name: filepath.ToSlash(rel),
			Mode: int64(info.Mode().Perm()),
			Size: info.Size(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// untarGz unpacks into destDir, refusing paths that escape it.
func untarGz(blob []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(hdr.Name)
		if filepath.IsAbs(name) || strings.Contains(name, "..") {
			return fmt.Errorf("tar entry %q escapes destination", hdr.Name)
		}
		target := filepath.Join(destDir, name)
		if hdr.Typeflag != tar.TypeReg {
			continue // packages contain only regular files
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode).Perm())
		if err != nil {
			return err
		}
		if _, err := io.CopyN(f, tr, hdr.Size); err != nil && err != io.EOF {
			f.Close()
			return err
		}
		f.Close()
	}
}
