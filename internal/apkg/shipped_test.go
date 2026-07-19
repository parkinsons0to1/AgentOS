package apkg

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShippedPackagesAreValid guards the packages/ and examples/packages/
// trees: every directory there must be an installable package.
func TestShippedPackagesAreValid(t *testing.T) {
	repoRoot := "../.."
	for _, tree := range []string{"packages", "examples/packages"} {
		entries, err := os.ReadDir(filepath.Join(repoRoot, tree))
		if err != nil {
			t.Fatalf("reading %s: %v", tree, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(repoRoot, tree, e.Name())
			m, err := LoadManifest(dir)
			if err != nil {
				t.Errorf("%s: %v", dir, err)
				continue
			}
			if m.Name != e.Name() {
				t.Errorf("%s: manifest name %q must match directory name", dir, m.Name)
			}
			if _, err := Install(dir, t.TempDir()); err != nil {
				t.Errorf("%s: install failed: %v", dir, err)
			}
		}
	}
}
