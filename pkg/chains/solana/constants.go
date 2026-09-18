package solana

// AdapterName is the name under which this package registers its chain
// factories with the chains registry.
const AdapterName = "solana"

// embeddedSDLPrefix is the literal collection prefix baked into the embedded
// .graphql files under collections/. CollectionSDL swaps it with the
// configured prefix at load time, mirroring the shared loader's embeddedPrefix
// behaviour for the EVM SDL. It coincides with DefaultCollectionPrefix.
const embeddedSDLPrefix = "Solana__Mainnet"
