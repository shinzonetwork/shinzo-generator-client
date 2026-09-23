package evm

import "strings"

type chainVariant uint8

const (
	ethereumVariant chainVariant = iota
	polygonVariant
)

func variantFromChainName(name string) chainVariant {
	if strings.EqualFold(strings.TrimSpace(name), "Polygon") {
		return polygonVariant
	}
	return ethereumVariant
}

func variantFromPrefix(prefix string) chainVariant {
	name, _, _ := strings.Cut(prefix, "__")
	return variantFromChainName(name)
}
