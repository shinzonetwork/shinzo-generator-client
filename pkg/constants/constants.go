package constants

// Schema-level document values shared across the stack.
const (
	// HeaderMagicValue is the magic marker at the head of every snapshot
	// file; readers match it to reject non-snapshot input before parsing.
	HeaderMagicValue = "DFKV"

	// BlockSignatureTypeValue is the value of the "type" discriminator on
	// block-signature JSONL entries; extractors match on it to pick
	// signature entries out of a mixed snapshot stream.
	BlockSignatureTypeValue = "block_signature"

	// MerkleRootFieldName is the document field carrying a signature's
	// Merkle root, shared by the block-signature and snapshot-signature
	// document shapes.
	MerkleRootFieldName = "merkleRoot"
)

// Field names of the signature documents (block-signature and
// snapshot-signature). These are the single source of truth for the
// document shape produced by the defra BlockHandler, the evm converter's
// signature builder, and the snapshot signer, so the three sites cannot
// drift apart.
const (
	// BlockNumberFieldName is the document field carrying a block's
	// number: the blockNumber field of the blockSignature schema (see the
	// shared SDL) and of the transaction, log, and access-list entry data
	// schemas, which share the same name.
	BlockNumberFieldName = "blockNumber"

	// BlockHashFieldName is the document field carrying a block's hash on
	// the data documents and on the block-signature document. See
	// BlockNumberFieldName for why the name is shared.
	BlockHashFieldName = "blockHash"

	// CIDCountFieldName is the field holding how many document CIDs the
	// signature covers.
	CIDCountFieldName = "cidCount"

	// CIDsFieldName is the field holding the ordered CID list signed over.
	CIDsFieldName = "cids"

	// SignatureTypeFieldName is the field naming the signature algorithm.
	SignatureTypeFieldName = "signatureType"

	// SignatureIdentityFieldName is the field carrying the signer identity.
	SignatureIdentityFieldName = "signatureIdentity"

	// SignatureValueFieldName is the field carrying the signature bytes.
	SignatureValueFieldName = "signatureValue"

	// CreatedAtFieldName is the field carrying the signing timestamp.
	CreatedAtFieldName = "createdAt"
)

// Block-document field names the generator and the host must agree on.
// The host prunes and bootstraps the Block collection by these fields, so
// their names are part of the generator-host contract alongside the
// signature-document fields above.
const (
	// NumberFieldName is the field of the Block document holding the
	// chain's block number ("number" in the block collection SDL).
	NumberFieldName = "number"

	// HashFieldName is the field holding a document's own hash; the Block
	// and Transaction documents share the name.
	HashFieldName = "hash"
)

// Signing key-algorithm identifiers used as signature-type values.
const (
	// Ed25519ValueString is the signature-type value for Ed25519 signers.
	Ed25519ValueString = "Ed25519"

	// Secp256k1ValueString is the signature-type value for ES256K
	// (secp256k1) signers.
	Secp256k1ValueString = "ES256K"
)

// Authentication modes for the schema endpoint.
const (
	// SchemaAuthModeNone disables authentication on the schema endpoint.
	SchemaAuthModeNone = "none"

	// SchemaAuthModeToken enables Bearer/API-key authentication on the
	// schema endpoint.
	SchemaAuthModeToken = "token"

	// SchemaAuthModeMTLS enables mTLS authentication on the schema endpoint
	// (not yet implemented).
	SchemaAuthModeMTLS = "mtls"
)

// HTTP response header values.
const (
	// ContentTypeJSON is the MIME type for JSON responses.
	ContentTypeJSON = "application/json"

	// CacheControlSchema is the Cache-Control directive for schema
	// responses, which are generated and must not be cached stale.
	CacheControlSchema = "no-cache"
)

// GeneratorVersion is the current version of the Shinzo Network Generator.
const GeneratorVersion = "0.6.5.4"
