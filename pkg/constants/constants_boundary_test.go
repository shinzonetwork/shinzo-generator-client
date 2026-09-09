package constants

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The 25 EVM field-name constants now live in pkg/chains/evm (constants.go).
// Two guards keep the chain-agnostic boundary intact:
//
//   - TestNoEVMFieldNamesOutsideEVM: no non-test .go file outside
//     pkg/chains/evm may reference any of the 25 names in any form
//     (constants.X, evm.X, or a bare identifier).
//   - TestConstantsPackageIsChainAgnostic: pkg/constants declares exactly
//     the 10 chain-agnostic constants; adding a constant here requires
//     updating that list.

// evmFieldNames are the Ethereum JSON-RPC field-name constants owned by
// pkg/chains/evm.
var evmFieldNames = []string{
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

// chainAgnosticNames are the only constants pkg/constants may declare.
var chainAgnosticNames = []string{
	"HeaderMagicValue",
	"BlockSignatureTypeValue",
	"MerkleRootKeyValue",
	"Ed25519ValueString",
	"Secp256k1ValueString",
	"SchemaAuthModeNone",
	"SchemaAuthModeToken",
	"SchemaAuthModeMTLS",
	"ContentTypeJSON",
	"CacheControlSchema",
}

// TestNoEVMFieldNamesOutsideEVM is the boundary guard of the chain-abstraction
// fixes: EVM field names may only be referenced from pkg/chains/evm. Non-test
// code outside that package must go through chains.DocumentGroup fields
// instead. Test files are exempt (fixtures legitimately name EVM fields).
func TestNoEVMFieldNamesOutsideEVM(t *testing.T) {
	t.Parallel()

	pattern, err := regexp.Compile(`\b(` + strings.Join(evmFieldNames, "|") + `)\b`)
	if err != nil {
		t.Fatalf("compile EVM field-name pattern: %v", err)
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
			for _, name := range pattern.FindAllString(line, -1) {
				violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, i+1, name))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo root %s: %v", root, walkErr)
	}
	if len(violations) > 0 {
		t.Errorf("EVM field-name constants referenced outside pkg/chains/evm in non-test code:\n%s",
			strings.Join(violations, "\n"))
	}
}

// TestConstantsPackageIsChainAgnostic asserts that pkg/constants declares
// exactly the chain-agnostic constant set — no EVM field names may return
// here, and no unreviewed additions may slip in.
func TestConstantsPackageIsChainAgnostic(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("constants.go")
	if err != nil {
		t.Fatalf("read constants.go: %v", err)
	}

	declRe := regexp.MustCompile(`(?m)^\s*(?:const\s+)?([A-Z]\w*)\s*=`)
	declared := map[string]bool{}
	for _, m := range declRe.FindAllStringSubmatch(string(data), -1) {
		declared[m[1]] = true
	}
	if len(declared) == 0 {
		t.Fatal("no constant declarations found in constants.go")
	}

	allowed := make(map[string]bool, len(chainAgnosticNames))
	for _, name := range chainAgnosticNames {
		allowed[name] = true
	}

	var unexpected, missing []string
	for name := range declared {
		if !allowed[name] {
			unexpected = append(unexpected, name)
		}
	}
	for _, name := range chainAgnosticNames {
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	if len(unexpected) > 0 || len(missing) > 0 {
		t.Errorf("pkg/constants must declare exactly the chain-agnostic constants %v; unexpected: %v; missing: %v",
			chainAgnosticNames, unexpected, missing)
	}
}
