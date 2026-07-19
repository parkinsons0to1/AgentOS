package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadValidSpec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.yaml")
	err := os.WriteFile(path, []byte(`
name: demo
runtime:
  command: ["bash", "-lc", "echo hi"]
budgets:
  wall_clock_seconds: 60
  max_egress_requests: 10
egress:
  - host: api.github.com
    secret: github-token
    inject:
      header: Authorization
      format: "Bearer {secret}"
  - host: api.example.com
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if g := s.GrantFor("API.GITHUB.COM"); g == nil || g.Secret != "github-token" {
		t.Fatal("GrantFor should match case-insensitively and carry the secret ref")
	}
	if s.GrantFor("evil.example.com") != nil {
		t.Fatal("ungranted host must return nil")
	}
}

func TestValidateRejectsBadSpecs(t *testing.T) {
	cases := map[string]Session{
		"missing name":    {Runtime: Runtime{Command: []string{"true"}}},
		"missing command": {Name: "x"},
		"url as host": {Name: "x", Runtime: Runtime{Command: []string{"true"}},
			Egress: []Grant{{Host: "https://api.github.com/v3"}}},
		"secret without inject": {Name: "x", Runtime: Runtime{Command: []string{"true"}},
			Egress: []Grant{{Host: "api.github.com", Secret: "tok"}}},
	}
	for name, s := range cases {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}
