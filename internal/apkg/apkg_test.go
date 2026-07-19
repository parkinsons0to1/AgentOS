package apkg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

// fixturePackage builds a valid package directory: a CLI, a skill, an MCP
// server, and an egress requirement.
func fixturePackage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string, mode os.FileMode) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("apkg.yaml", `
name: greeter
version: 1.0.0
description: Test package
provides:
  clis: [hello]
  skills: [greeting.md]
  mcp_servers:
    - name: greet
      command: ["python3", "server.py"]
requires:
  egress:
    - host: api.greetings.example
      secret: greet-token
      inject:
        header: Authorization
        format: "Bearer {secret}"
`, 0o644)
	write("bin/hello", "#!/bin/sh\necho hello from package\n", 0o755)
	write("skills/greeting.md", "# Greeting skill\n", 0o644)
	return dir
}

func TestInstallListResolve(t *testing.T) {
	store := t.TempDir()
	p, err := Install(fixturePackage(t), store)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "greeter" || p.Dir != filepath.Join(store, "greeter") {
		t.Fatalf("unexpected install result: %+v", p)
	}
	// The copied CLI must stay executable.
	info, err := os.Stat(filepath.Join(p.Dir, "bin", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatal("installed CLI lost its executable bit")
	}

	pkgs, err := List(store)
	if err != nil || len(pkgs) != 1 || pkgs[0].Name != "greeter" {
		t.Fatalf("List = %v, %v", pkgs, err)
	}

	if _, err := Resolve(store, []string{"greeter"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(store, []string{"missing"}); err == nil {
		t.Fatal("resolving a missing package must fail")
	}
}

func TestValidateCatchesMissingDeclaredFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "apkg.yaml"), []byte(`
name: broken
version: 1.0.0
provides:
  clis: [ghost]
`), 0o644)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("manifest declaring a missing CLI must fail validation")
	}
}

func TestApplyMergesGrantsToolsAndPaths(t *testing.T) {
	store := t.TempDir()
	p, err := Install(fixturePackage(t), store)
	if err != nil {
		t.Fatal(err)
	}
	sess := &spec.Session{
		Name:    "s",
		Runtime: spec.Runtime{Command: []string{"true"}},
		// Session already grants the host: Apply must not duplicate it.
		Egress: []spec.Grant{{Host: "api.greetings.example"}},
	}
	binDirs, err := Apply(sess, []*Package{p})
	if err != nil {
		t.Fatal(err)
	}
	if len(binDirs) != 1 || binDirs[0] != filepath.Join(p.Dir, "bin") {
		t.Fatalf("binDirs = %v", binDirs)
	}
	if len(sess.Egress) != 1 {
		t.Fatalf("grant was duplicated: %+v", sess.Egress)
	}
	if len(sess.MCPServers) != 1 || sess.MCPServers[0].Name != "greet" {
		t.Fatalf("mcp servers not merged: %+v", sess.MCPServers)
	}
}

func TestApplyRejectsMCPNameCollision(t *testing.T) {
	store := t.TempDir()
	p, err := Install(fixturePackage(t), store)
	if err != nil {
		t.Fatal(err)
	}
	sess := &spec.Session{
		Name:       "s",
		Runtime:    spec.Runtime{Command: []string{"true"}},
		MCPServers: []spec.MCPServer{{Name: "greet", Command: []string{"true"}}},
	}
	if _, err := Apply(sess, []*Package{p}); err == nil {
		t.Fatal("colliding MCP server names must be an error")
	}
}
