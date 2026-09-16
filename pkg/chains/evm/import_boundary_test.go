package evm_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoChainsEVMImportsOutsideEVMAndCmd is the structural boundary guard of
// the chain-abstraction fixes: any evm.X reference requires importing this
// package (the compiler enforces that), so checking the import path alone is
// alias-proof, rename-proof, and covers every constant and identifier the
// package declares — now and in the future. All other code must consume the
// chain through the pkg/chains interfaces instead.
//
// Exemptions:
//   - pkg/chains/evm itself (the guarded package);
//   - cmd/ — binaries are composition roots that import an adapter so its
//     init() runs RegisterChain;
//   - _test.go files — test fixtures legitimately import the adapter;
//   - .git, vendor, node_modules.
func TestNoChainsEVMImportsOutsideEVMAndCmd(t *testing.T) {
	t.Parallel()

	const evmImportPath = `"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"`

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	var violations []string
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			switch rel {
			case ".git", "vendor", "node_modules", "cmd", filepath.Join("pkg", "chains", "evm"):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, evmImportPath) {
				violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, i+1, evmImportPath))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo root %s: %v", root, walkErr)
	}
	if len(violations) > 0 {
		t.Errorf("pkg/chains/evm imported outside pkg/chains/evm and cmd/ in non-test code:\n%s",
			strings.Join(violations, "\n"))
	}
}
