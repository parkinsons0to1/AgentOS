// Package spec defines the session spec: the declarative contract that tells
// agentd what runtime to launch, what it may reach, and what it may spend.
package spec

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Session is the top-level session spec, loaded from YAML.
type Session struct {
	Name       string            `yaml:"name"`
	Runtime    Runtime           `yaml:"runtime"`
	Workspace  string            `yaml:"workspace"`
	Budgets    Budgets           `yaml:"budgets"`
	Egress     []Grant           `yaml:"egress"`
	MCPServers []MCPServer       `yaml:"mcp_servers"`
	Packages   []string          `yaml:"packages"`
	Env        map[string]string `yaml:"env"`
}

// Runtime is the agent process to supervise (any harness: Claude Code,
// OpenClaw, a bare SDK loop, or a shell for testing).
type Runtime struct {
	Command []string `yaml:"command"`
}

// Budgets are hard limits enforced outside the agent's trust boundary.
// Zero values mean "unlimited" except where noted.
type Budgets struct {
	WallClockSeconds  int `yaml:"wall_clock_seconds"`
	MaxEgressRequests int `yaml:"max_egress_requests"`
	MaxToolCalls      int `yaml:"max_tool_calls"`
}

// MCPServer declares one MCP server available to the session via the toolbus.
// Servers are spawned lazily on first use and speak JSON-RPC over stdio.
type MCPServer struct {
	Name    string            `yaml:"name"`
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`
	// Dir is the working directory the server is spawned in. Packages set
	// this to their install dir so relative paths in Command resolve.
	Dir string `yaml:"dir"`
}

// Grant is a capability grant for one upstream host. The secret named here is
// resolved by capd at proxy time; it never enters the session environment.
type Grant struct {
	Host   string  `yaml:"host"`
	Secret string  `yaml:"secret"`
	Inject *Inject `yaml:"inject"`
	// Upstream overrides the forward target (scheme://host:port). Used for
	// local MCP/dev servers and for tests; empty means https://<Host>.
	Upstream string `yaml:"upstream"`
}

// Inject describes how the secret is attached to outbound requests.
type Inject struct {
	Header string `yaml:"header"`
	Format string `yaml:"format"` // e.g. "Bearer {secret}"; "{secret}" if empty
}

// Load reads and validates a session spec from a YAML file.
func Load(path string) (*Session, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("invalid spec %s: %w", path, err)
	}
	return &s, nil
}

// Validate enforces the invariants the rest of the system relies on.
func (s *Session) Validate() error {
	if s.Name == "" {
		return errors.New("name is required")
	}
	if len(s.Runtime.Command) == 0 {
		return errors.New("runtime.command is required")
	}
	for i, g := range s.Egress {
		if g.Host == "" {
			return fmt.Errorf("egress[%d]: host is required", i)
		}
		if strings.Contains(g.Host, "/") {
			return fmt.Errorf("egress[%d]: host %q must be a bare hostname, not a URL", i, g.Host)
		}
		if g.Secret != "" && (g.Inject == nil || g.Inject.Header == "") {
			return fmt.Errorf("egress[%d]: secret %q needs inject.header", i, g.Secret)
		}
	}
	seen := map[string]bool{}
	for i, m := range s.MCPServers {
		if m.Name == "" || strings.ContainsAny(m.Name, "/ ") {
			return fmt.Errorf("mcp_servers[%d]: name must be non-empty with no slashes or spaces", i)
		}
		if seen[m.Name] {
			return fmt.Errorf("mcp_servers[%d]: duplicate name %q", i, m.Name)
		}
		seen[m.Name] = true
		if len(m.Command) == 0 {
			return fmt.Errorf("mcp_servers[%d] (%s): command is required", i, m.Name)
		}
	}
	return nil
}

// GrantFor returns the grant matching host, or nil if egress is denied.
func (s *Session) GrantFor(host string) *Grant {
	for i := range s.Egress {
		if strings.EqualFold(s.Egress[i].Host, host) {
			return &s.Egress[i]
		}
	}
	return nil
}
