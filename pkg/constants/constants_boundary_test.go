package constants_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoEVMConstantReferencesOutsideEVM is the reference-list boundary guard
// (PR 1 guard step of the chain-abstraction fixes): none of the 25 EVM
// field-name constants may be referenced outside pkg/chains/evm in non-test
// code. pkg/defra reads field names from chains.DocumentGroup instead.
//
// When the constants physically move into pkg/chains/evm, this guard becomes
// structural: no pkg/chains/evm import outside itself in non-test code.
func TestNoEVMConstantReferencesOutsideEVM(t *testing.T) {
	t.Parallel()

	// The 25 EVM field-name constants: 22 JSON-RPC names slated to move in
	// PR 2, plus the 3 former mixed constants decoupled from pkg/defra.
	evmNames := []string{
		"NumberFieldValue",
		"HashKeyValue",
		"BlockNumberKeyValue",
		"BlockHashKeyValue",
		"AddressKeyValue",
		"TransactionHashKeyValue",
		"TimestampKeyValue",
		"ParentHashKeyValue",
		"DifficultyKeyValue",
		"GasUsedKeyValue",
		"GasLimitKeyValue",
		"NonceKeyValue",
		"MinerKeyValue",
		"StateRootKeyValue",
		"Sha3UnclesKeyValue",
		"TransactionsRootKeyValue",
		"ReceiptsRootKeyValue",
		"LogsBloomKeyValue",
		"ExtraDataKeyValue",
		"MixHashKeyValue",
		"TransactionIndexKeyValue",
		"TypeKeyValue",
		"CumulativeGasUsedKeyValue",
		"EffectiveGasPriceKeyValue",
		"StatusKeyValue",
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	evmDir := filepath.Join("pkg", "chains", "evm")

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
			case ".git", "vendor", "node_modules", evmDir:
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
			for _, name := range evmNames {
				ref := "constants." + name
				if strings.Contains(line, ref) {
					violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, i+1, ref))
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo root %s: %v", root, walkErr)
	}
	if len(violations) > 0 {
		t.Errorf("EVM constants referenced outside pkg/chains/evm in non-test code:\n%s",
			strings.Join(violations, "\n"))
	}
}
