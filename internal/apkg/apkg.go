// Package apkg implements the AgentOS package format: an "app" for an agent
// is not a windowed binary but a bundle of capabilities-to-act — CLIs, MCP
// servers, skills (prompt-space instructions), and the egress grants they
// need. The manifest's requires block is the permission prompt: installing
// and referencing a package is the operator's consent, Android-style, and
// nothing in a package runs with more than it declared.
//
// Layout of a package directory:
//
//	apkg.yaml     — the manifest
//	bin/          — CLIs, prepended to the session PATH
//	skills/       — markdown workflows, listed in the session manifest
package apkg

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

// Manifest is the contents of apkg.yaml.
type Manifest struct {
	Name        string   `yaml:"name"`
	Version     string   `yaml:"version"`
	Description string   `yaml:"description"`
	Provides    Provides `yaml:"provides"`
	Requires    Requires `yaml:"requires"`
}

// Provides declares what the package adds to a session.
type Provides struct {
	CLIs       []string         `yaml:"clis"` // names expected under bin/
	MCPServers []spec.MCPServer `yaml:"mcp_servers"`
	Skills     []string         `yaml:"skills"` // paths under skills/
}

// Requires declares the capabilities the package needs. Secret values are
// never part of a package — only the secret *names* the broker must hold.
type Requires struct {
	Egress []spec.Grant `yaml:"egress"`
}

// Package is an installed package: its manifest plus its directory in the store.
type Package struct {
	Manifest
	Dir string
}

// LoadManifest reads and validates a package directory's apkg.yaml.
func LoadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "apkg.yaml"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing apkg.yaml: %w", err)
	}
	if err := m.Validate(dir); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the manifest and that declared files actually exist in dir.
func (m *Manifest) Validate(dir string) error {
	if m.Name == "" || strings.ContainsAny(m.Name, "/ ") {
		return errors.New("apkg: name must be non-empty with no slashes or spaces")
	}
	if m.Version == "" {
		return errors.New("apkg: version is required")
	}
	for _, cli := range m.Provides.CLIs {
		if strings.Contains(cli, "/") {
			return fmt.Errorf("apkg: cli %q must be a bare name under bin/", cli)
		}
		if _, err := os.Stat(filepath.Join(dir, "bin", cli)); err != nil {
			return fmt.Errorf("apkg: declared cli %q missing from bin/: %w", cli, err)
		}
	}
	for _, sk := range m.Provides.Skills {
		if _, err := os.Stat(filepath.Join(dir, "skills", sk)); err != nil {
			return fmt.Errorf("apkg: declared skill %q missing from skills/: %w", sk, err)
		}
	}
	for i, ms := range m.Provides.MCPServers {
		if ms.Name == "" || len(ms.Command) == 0 {
			return fmt.Errorf("apkg: mcp_servers[%d] needs name and command", i)
		}
	}
	for i, g := range m.Requires.Egress {
		if g.Host == "" || strings.Contains(g.Host, "/") {
			return fmt.Errorf("apkg: requires.egress[%d]: host must be a bare hostname", i)
		}
		if g.Secret != "" && (g.Inject == nil || g.Inject.Header == "") {
			return fmt.Errorf("apkg: requires.egress[%d]: secret %q needs inject.header", i, g.Secret)
		}
	}
	return nil
}

// Install validates srcDir and copies it into the store under the package
// name, replacing any previous version. It returns the installed package.
func Install(srcDir, storeDir string) (*Package, error) {
	m, err := LoadManifest(srcDir)
	if err != nil {
		return nil, err
	}
	dst := filepath.Join(storeDir, m.Name)
	if err := os.RemoveAll(dst); err != nil {
		return nil, err
	}
	if err := copyTree(srcDir, dst); err != nil {
		return nil, err
	}
	// Some transports (zip files, editors) drop the executable bit; declared
	// CLIs are executable by contract, so restore it at install time.
	for _, cli := range m.Provides.CLIs {
		if err := os.Chmod(filepath.Join(dst, "bin", cli), 0o755); err != nil {
			return nil, err
		}
	}
	return &Package{Manifest: *m, Dir: dst}, nil
}

// List returns all installed packages in the store.
func List(storeDir string) ([]*Package, error) {
	entries, err := os.ReadDir(storeDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pkgs []*Package
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(storeDir, e.Name())
		m, err := LoadManifest(dir)
		if err != nil {
			return nil, fmt.Errorf("package %s is corrupt: %w", e.Name(), err)
		}
		pkgs = append(pkgs, &Package{Manifest: *m, Dir: dir})
	}
	return pkgs, nil
}

// Resolve looks up the named packages in the store.
func Resolve(storeDir string, names []string) ([]*Package, error) {
	var pkgs []*Package
	for _, name := range names {
		dir := filepath.Join(storeDir, name)
		m, err := LoadManifest(dir)
		if err != nil {
			return nil, fmt.Errorf("package %q is not installed (agentd pkg install ...): %w", name, err)
		}
		pkgs = append(pkgs, &Package{Manifest: *m, Dir: dir})
	}
	return pkgs, nil
}

// Apply merges the packages into the session: MCP servers and egress grants
// are appended (referencing an installed package is the operator's consent to
// its declared requirements), and the returned paths are prepended to PATH.
// MCP server name collisions are an error rather than a silent override.
func Apply(sess *spec.Session, pkgs []*Package) (binDirs []string, err error) {
	serverNames := map[string]string{} // name -> origin
	for _, ms := range sess.MCPServers {
		serverNames[ms.Name] = "session spec"
	}
	for _, p := range pkgs {
		for _, ms := range p.Provides.MCPServers {
			if origin, dup := serverNames[ms.Name]; dup {
				return nil, fmt.Errorf("mcp server %q from package %s collides with %s", ms.Name, p.Name, origin)
			}
			serverNames[ms.Name] = "package " + p.Name
			if ms.Dir == "" {
				ms.Dir = p.Dir // relative paths in Command resolve in the package
			}
			sess.MCPServers = append(sess.MCPServers, ms)
		}
		for _, g := range p.Requires.Egress {
			if sess.GrantFor(g.Host) == nil {
				sess.Egress = append(sess.Egress, g)
			}
		}
		if len(p.Provides.CLIs) > 0 {
			binDirs = append(binDirs, filepath.Join(p.Dir, "bin"))
		}
	}
	return binDirs, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
