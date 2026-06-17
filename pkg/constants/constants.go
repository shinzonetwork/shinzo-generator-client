package constants

// HeaderMagicValue is the current magic value assigned to the header.
const HeaderMagicValue = "DFKV"

// NumberFieldValue is the string value "number" assigned to a field.
const NumberFieldValue = "number"

// BlockSignatureTypeValue is the string value "block_signature" assigned to a type field.
const BlockSignatureTypeValue = "block_signature"

// MerkleRootKeyValue is the string value "merkleRoot" assigned to a key field.
const MerkleRootKeyValue = "merkleRoot"

// BlockHashKeyValue is the string value "blockHash" assigned to a key field.
const BlockHashKeyValue = "blockHash"

// BlockNumberKeyValue is the string value "blockNumber" assigned to a key field.
const BlockNumberKeyValue = "blockNumber"

// Ed25519ValueString is the string value "Ed25519" assigned to a key field.
const Ed25519ValueString = "Ed25519"

// Secp256k1ValueString is the string value "ES256K" assigned to a key field.
const Secp256k1ValueString = "ES256K"

// SchemaAuthModeNone disables authentication on the schema endpoint.
const SchemaAuthModeNone = "none"

// SchemaAuthModeToken enables Bearer/API-key authentication on the schema endpoint.
const SchemaAuthModeToken = "token"

// SchemaAuthModeMTLS enables mTLS authentication on the schema endpoint (not yet implemented).
const SchemaAuthModeMTLS = "mtls"

// ContentTypeJSON is the MIME type for JSON responses.
const ContentTypeJSON = "application/json"

// ContentTypePlain is the MIME type for plain text responses.
const ContentTypePlain = "text/plain"

// AcceptHeaderMaxParts is the maximum number of parts when splitting an Accept header on semicolons.
const AcceptHeaderMaxParts = 2

// CacheControlSchema is the Cache-Control directive for schema responses.
const CacheControlSchema = "no-cache"
