package evm

import (
	"strconv"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
)

// chainVariant identifies which document shape and schema file set a chain
// uses. It is resolved once from chain.name at construction time; the
// converter and the collections both read the resolved variant, so the
// switch lives in exactly one place.
type chainVariant int

const (
	variantEthereum chainVariant = iota
	variantPolygon
)

// resolveVariant maps a chain name to its variant. Only "polygon" (any case,
// surrounding spaces ignored) selects the Polygon variant. Everything else,
// including empty, keeps the Ethereum shape, so existing configs and tests
// (Arbitrum, Optimism) see no change.
func resolveVariant(chainName string) chainVariant {
	if strings.EqualFold(strings.TrimSpace(chainName), "polygon") {
		return variantPolygon
	}
	return variantEthereum
}

// variantFromPrefix resolves the variant from a collection prefix
// ("Polygon__Mainnet" -> "Polygon"). The prefix derives from chain.name, so
// this always agrees with resolveVariant on the config value.
func variantFromPrefix(prefix string) chainVariant {
	name, _, _ := strings.Cut(prefix, "__")
	return resolveVariant(name)
}

// buildPolygonBlockData composes the Ethereum block builder, then drops
// totalDifficulty: it is dead (hardcoded "" at the client) and absent from
// the Polygon schema.
func (c *Converter) buildPolygonBlockData(block *Block, blockInt int64) map[string]any {
	d := c.buildBlockData(block, blockInt)
	delete(d, "totalDifficulty")
	return d
}

// buildPolygonTransactionData composes the Ethereum transaction builder, then
// drops the dead receipt fields (status, cumulativeGasUsed, effectiveGasPrice
// are never populated from receipts) and adds yParity.
func (c *Converter) buildPolygonTransactionData(tx *Transaction) map[string]any {
	d := c.buildTransactionData(tx)
	delete(d, constants.StatusKeyValue)
	delete(d, constants.CumulativeGasUsedKeyValue)
	delete(d, constants.EffectiveGasPriceKeyValue)
	d["yParity"] = yParity(tx.Type, tx.V)
	return d
}

// yParity derives the Polygon yParity field from a transaction's type and v.
// Production values are decimal strings (convertTransaction); hex spellings
// are accepted too. Legacy txs (type 0) and unrecognized types get "".
// Typed txs (1, 2, 126, and 127 for Bor state-sync, seen on live mainnet)
// map v of 0/27 to "0" and 1/28 to "1"; anything else gets "".
func yParity(txType, v string) string {
	t, err := strconv.ParseInt(txType, 0, 64)
	if err != nil {
		return ""
	}
	switch t {
	case 0:
		return ""
	case 1, 2, 126, 127: // 0x1, 0x2, 0x7e, 0x7f
	default:
		return ""
	}
	vv, err := strconv.ParseInt(v, 0, 64)
	if err != nil {
		return ""
	}
	switch vv {
	case 0, 27: // 0x0, 0x1b
		return "0"
	case 1, 28: // 0x1, 0x1c
		return "1"
	}
	return ""
}
