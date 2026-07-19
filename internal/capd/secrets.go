package capd

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Secrets is the broker's credential store. It is loaded by agentd from an
// operator-owned file and lives only in the supervisor process — secrets are
// never written into the session environment, filesystem, or ledger.
type Secrets struct {
	values map[string]string
}

// LoadSecrets reads a flat name->value YAML file. The file must not be
// group/world readable; refusing loose permissions keeps the invariant
// that only the operator and the broker can read raw credentials.
func LoadSecrets(path string) (*Secrets, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("secrets file %s has mode %04o; must be 0600 (chmod 600 %s)", path, perm, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &Secrets{values: values}, nil
}

// NewSecrets builds a store from memory (for tests).
func NewSecrets(values map[string]string) *Secrets {
	return &Secrets{values: values}
}

// Get returns the named secret. Missing secrets are an error, not an empty
// string — silently sending unauthenticated requests would mask misconfig.
func (s *Secrets) Get(name string) (string, error) {
	v, ok := s.values[name]
	if !ok {
		return "", fmt.Errorf("secret %q not found in store", name)
	}
	return v, nil
}
