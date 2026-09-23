package evm

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func polygonTestConfig() *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Name:    "Polygon",
			Network: "Mainnet",
			Adapter: config.DefaultChainAdapter,
		},
	}
}

func TestPolygonYParity(t *testing.T) {
	t.Parallel()

	baseTests := []struct {
		name   string
		txType string
		v      string
		want   string
	}{
		{name: "legacy", txType: "0x0", v: "0x1b", want: ""},
		{name: "decimal client values", txType: "126", v: "28", want: "1"},
		{name: "unrecognized type", txType: "0x3", v: "0x0", want: ""},
		{name: "unrecognized v", txType: "0x2", v: "0x2", want: ""},
		{name: "malformed type", txType: "wat", v: "0x0", want: ""},
		{name: "malformed v", txType: "0x2", v: "wat", want: ""},
	}

	for _, tc := range baseTests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, polygonYParity(tc.txType, tc.v))
		})
	}

	for _, txType := range []string{"0x1", "0x2", "0x7e"} {
		for _, parity := range []struct {
			v    string
			want string
		}{
			{v: "0x0", want: "0"},
			{v: "0x1", want: "1"},
			{v: "0x1b", want: "0"},
			{v: "0x1c", want: "1"},
		} {
			t.Run(txType+"_v_"+parity.v, func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, parity.want, polygonYParity(txType, parity.v))
			})
		}
	}
}

func TestEthereumVariantRegressionFields(t *testing.T) {
	t.Parallel()

	c := NewConverter(testConfig())
	blockDoc := c.buildBlockData(fakeBlock(42), 42)
	tx := fakeTx(fakeHash("ethereum-regression"))
	txDoc := c.buildTransactionData(&tx)

	assert.Contains(t, blockDoc, "totalDifficulty")
	assert.Contains(t, txDoc, constants.StatusKeyValue)
	assert.Contains(t, txDoc, constants.CumulativeGasUsedKeyValue)
	assert.Contains(t, txDoc, constants.EffectiveGasPriceKeyValue)
	assert.NotContains(t, txDoc, "yParity")
}

func TestPolygonVariantDocumentShape(t *testing.T) {
	t.Parallel()

	c := NewConverter(polygonTestConfig())
	blockDoc := c.buildBlockData(fakeBlock(42), 42)
	tx := fakeTx(fakeHash("polygon-shape"))
	tx.Type = "0x2"
	tx.V = "0x1"
	tx.GasUsed = "21000"
	txDoc := c.buildTransactionData(&tx)

	assert.NotContains(t, blockDoc, "totalDifficulty")
	assert.NotContains(t, txDoc, constants.StatusKeyValue)
	assert.NotContains(t, txDoc, constants.CumulativeGasUsedKeyValue)
	assert.NotContains(t, txDoc, constants.EffectiveGasPriceKeyValue)
	assert.NotContains(t, txDoc, constants.GasUsedKeyValue)
	assert.Equal(t, "1", txDoc["yParity"])
}

func TestPolygonVariantConversionSetup(t *testing.T) {
	t.Parallel()

	c := NewConverter(polygonTestConfig())
	tx := fakeTx(fakeHash("polygon-conversion"))
	tx.Type = "126"
	tx.V = "27"
	bundle := &BlockBundle{
		Block:        fakeBlockWithTxs(42, tx),
		Transactions: []*Transaction{&tx},
	}

	result, err := c.Convert(context.Background(), bundle)
	require.NoError(t, err)
	require.Len(t, result.Groups, 2)
	assert.Equal(t, "Polygon__Mainnet__Block", result.Groups[0].Collection)
	assert.Equal(t, "Polygon__Mainnet__Transaction", result.Groups[1].Collection)
	assert.Equal(t, "0", result.Groups[1].Docs[0]["yParity"])
	assert.Equal(t, "Polygon__Mainnet__BlockSignature", result.SignatureCollection)
}

func TestPolygonSDLMatchesConverterFields(t *testing.T) {
	t.Parallel()

	c := NewConverter(polygonTestConfig())
	sdl, err := c.GetSchema()
	require.NoError(t, err)

	blockFields := scalarFieldsForType(t, sdl, c.collections.Block)
	txFields := scalarFieldsForType(t, sdl, c.collections.Transaction)

	blockDoc := c.buildBlockData(fakeBlock(42), 42)
	tx := fakeTx(fakeHash("polygon-sdl-parity"))
	tx.Type = "0x7e"
	tx.V = "0x1c"
	txDoc := c.buildTransactionData(&tx)

	assert.ElementsMatch(t, mapKeys(blockDoc), blockFields)
	assert.ElementsMatch(t, mapKeys(txDoc), txFields)
	assert.NotContains(t, sdl, "totalDifficulty:")
	transactionSDL := regexp.MustCompile(`(?s)type\s+` + regexp.QuoteMeta(c.collections.Transaction) + `\s*\{(.*?)\}`).FindString(sdl)
	assert.NotContains(t, transactionSDL, "gasUsed:")
}

func scalarFieldsForType(t *testing.T, sdl, typeName string) []string {
	t.Helper()

	typePattern := regexp.MustCompile(`(?s)type\s+` + regexp.QuoteMeta(typeName) + `\s*\{(.*?)\}`)
	match := typePattern.FindStringSubmatch(sdl)
	require.Len(t, match, 2, "type %s not found in SDL", typeName)

	var fields []string
	for _, line := range strings.Split(match[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "@relation") {
			continue
		}
		name, _, ok := strings.Cut(line, ":")
		if ok {
			fields = append(fields, strings.TrimSpace(name))
		}
	}
	return fields
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
