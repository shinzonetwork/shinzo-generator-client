package defra

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	codecsig "github.com/shinzonetwork/shinzo-evidence-codec/sig"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// tokenNode starts a node with a node identity of its own, as a generator's node runs, holding the
// tokens-mode schema for mockReceipt's log contract, and returns it with the matching converter.
func tokenNode(t *testing.T) (*testutils.TestDefraDB, *evm.Converter) {
	t.Helper()
	conv, err := evm.NewConverter(&config.Config{Chain: config.ChainConfig{
		Name: "Bsc", Network: "Mainnet",
		Mode: config.ChainModeTokens, Tokens: []config.Token{{Address: "0x0000000000000000000000000000000000000003"}},
	}})
	require.NoError(t, err)
	sdl, err := conv.GetSchema()
	require.NoError(t, err)
	ident, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	return testutils.SetupTestDefraDBWithSchemaOpts(t, sdl, t.TempDir(),
		func(nb *options.NodeOptionsBuilder) { nb.DB().SetNodeIdentity(ident) }), conv
}

// tokenCapture is a supply capture for mockReceipt's log contract.
var tokenCapture = evm.StateCapture{Asset: "0000000000000000000000000000000000000003", Value: big.NewInt(5), Proof: []byte{1}}

// convertTokenBlock converts mockReceipt's block in tokens mode, with the given supply captures.
func convertTokenBlock(t *testing.T, conv *evm.Converter, captures ...evm.StateCapture) chains.ConversionResult {
	t.Helper()
	const txHash = "0xddd1000000000000000000000000000000000000000000000000000000000001"
	block := mockBlock("0x7D0") // 2000
	block.Header = &ethtypes.Header{Number: big.NewInt(2000), Difficulty: big.NewInt(0)}
	block.GethTransactions = ethtypes.Transactions{ethtypes.NewTx(&ethtypes.LegacyTx{
		To: &common.Address{2}, Value: big.NewInt(0), Gas: 21000, GasPrice: big.NewInt(1),
	})}
	receipt := mockReceipt(txHash, "0x7D0")
	receipt.GethReceipt = &ethtypes.Receipt{
		Status: ethtypes.ReceiptStatusSuccessful, CumulativeGasUsed: 21000,
		Logs: []*ethtypes.Log{{Address: common.HexToAddress(receipt.Logs[0].Address)}},
	}
	result, err := conv.Convert(context.Background(), &evm.BlockBundle{
		Block:         block,
		Transactions:  []*evm.Transaction{mockTransaction(txHash, "2000")},
		Receipts:      []*evm.TransactionReceipt{receipt},
		StateCaptures: captures,
	})
	require.NoError(t, err)
	return result
}

// storeTokenBlock stores mockReceipt's block in tokens mode with a new identity and returns the
// identity, the conversion it stored and the store's result.
func storeTokenBlock(t *testing.T, td *testutils.TestDefraDB, conv *evm.Converter) (identity.Identity, chains.ConversionResult, *BlockCreationResult) {
	t.Helper()
	ident, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	result := convertTokenBlock(t, conv, tokenCapture)
	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)
	res, err := handler.Store(defracontext.WithIdentity(context.Background(), ident), result)
	require.NoError(t, err)
	return ident, result, res
}

func TestStore_TokenModeAttestsTheStatement(t *testing.T) {
	t.Parallel()
	td, conv := tokenNode(t)
	ident, result, res := storeTokenBlock(t, td, conv)

	attestations := conv.Collections().BlockSignatureCollection()
	query := fmt.Sprintf(`query { %s(docID: %q) { blockNumber blockHash statement signatureType signer signature } }`,
		attestations, res.BlockSignatureID)
	resp := td.Node.DB.ExecRequest(context.Background(), query)
	require.Empty(t, resp.GQL.Errors)
	docs := resp.GQL.Data.(map[string]any)[attestations].([]map[string]any)
	require.Len(t, docs, 1)
	doc := docs[0]

	assert.EqualValues(t, 2000, doc[constants.BlockNumberFieldName])
	assert.Equal(t, result.Groups[0].Docs[0][constants.HashFieldName], doc[constants.BlockHashFieldName])
	assert.Equal(t, hex.EncodeToString(result.Statement), doc[constants.StatementFieldName])
	assert.Equal(t, codecsig.TypeES256K, doc[constants.SignatureTypeFieldName])
	assert.Equal(t, ident.PublicKey().String(), doc[constants.SignerFieldName])
	require.NoError(t, codecsig.VerifyStatement(result.Statement, doc[constants.SignerFieldName].(string),
		codecsig.TypeES256K, doc[constants.SignatureFieldName].(string)))
}

// Generators with different node identities write the same documents, so their document IDs match.
func TestStore_TokenDocumentsIdenticalAcrossNodes(t *testing.T) {
	t.Parallel()
	var docIDs [2][]string
	for i := range 2 {
		td, conv := tokenNode(t)
		_, _, res := storeTokenBlock(t, td, conv)
		docIDs[i] = append(docIDs[i], res.BlockID)
		for _, ids := range res.OtherDocIDs {
			docIDs[i] = append(docIDs[i], ids...)
		}
		slices.Sort(docIDs[i])
	}
	require.Len(t, docIDs[0], 3, "the header, the token's Evidence document and its supply capture")
	assert.Equal(t, docIDs[0], docIDs[1])
}

// Every document a token block stores is tracked for pruning: the header, the Evidence and
// StateCapture documents, and the attestation.
func TestStore_TokenModeTracksEveryDocument(t *testing.T) {
	t.Parallel()
	td, conv := tokenNode(t)
	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)
	tracker := &mockDocIDTracker{}
	handler.SetDocIDTracker(tracker)
	_, err = handler.Store(ctxWithIdentity(t), convertTokenBlock(t, conv, tokenCapture))
	require.NoError(t, err)

	require.Len(t, tracker.trackedResults, 1)
	tracked := tracker.trackedResults[0]
	trackedIDs := maps.Clone(tracked.OtherDocIDs)
	trackedIDs[conv.Collections().BlockCollection()] = []string{tracked.BlockID}
	trackedIDs[conv.Collections().BlockSignatureCollection()] = []string{tracked.BlockSignatureID}
	stored, err := conv.GetDocIDsByBlockRange(context.Background(), td.Node, 2000, 2000)
	require.NoError(t, err)
	require.Len(t, stored, 4)
	assert.Equal(t, stored, trackedIDs)
}

// countDocs returns how many documents collection holds.
func countDocs(t *testing.T, td *testutils.TestDefraDB, collection string) int {
	t.Helper()
	res := td.Node.DB.ExecRequest(context.Background(), "query { "+collection+" { _docID } }")
	require.Empty(t, res.GQL.Errors)
	switch docs := res.GQL.Data.(map[string]any)[collection].(type) {
	case []map[string]any:
		return len(docs)
	case []any:
		return len(docs)
	}
	return 0
}

// An attempt without a valid attestation stores neither the header nor the attestation, and a later
// attempt stores the block.
func TestStore_TokenModeRetryAfterUnattestedAttempt(t *testing.T) {
	t.Parallel()
	// This P-256 key's public key is not a secp256k1 point, so the codec refuses it as the signer.
	key, err := crypto.PrivateKeyFromBytes(crypto.KeyTypeSecp256r1, bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	p256, err := identity.FromPrivateKey(key)
	require.NoError(t, err)
	cases := []struct {
		name    string
		ctx     context.Context
		wantErr error
	}{
		{name: "no identity to sign with", ctx: context.Background(), wantErr: errNoIdentity},
		{name: "secp256r1 identity", ctx: defracontext.WithIdentity(context.Background(), p256), wantErr: codecsig.ErrSigner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			td, conv := tokenNode(t)
			header, attestation := conv.Collections().BlockCollection(), conv.Collections().BlockSignatureCollection()
			handler, err := NewBlockHandler(td.Node, 1000)
			require.NoError(t, err)
			_, err = handler.Store(tc.ctx, convertTokenBlock(t, conv, tokenCapture))
			require.ErrorIs(t, err, tc.wantErr)
			assert.Zero(t, countDocs(t, td, header), "no header without an attestation")
			assert.Zero(t, countDocs(t, td, attestation))

			_, err = handler.Store(ctxWithIdentity(t), convertTokenBlock(t, conv, tokenCapture))
			require.NoError(t, err)
			for _, name := range conv.GetCollections() {
				assert.Equal(t, 1, countDocs(t, td, name), name)
			}
		})
	}
}

// A capture written by an attempt that could not be attested is purged by the next attempt, even
// when that attempt has no capture of its own.
func TestStore_TokenModeRetryPurgesLeftoverCapture(t *testing.T) {
	t.Parallel()
	td, conv := tokenNode(t)
	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)
	first := convertTokenBlock(t, conv, tokenCapture)
	captures := first.Groups[2].Collection
	// With no identity in ctx the statement cannot be signed.
	_, err = handler.Store(context.Background(), first)
	require.ErrorIs(t, err, errNoIdentity)
	require.Equal(t, 1, countDocs(t, td, captures), "the failed attempt left its capture")

	_, err = handler.Store(ctxWithIdentity(t), convertTokenBlock(t, conv))
	require.NoError(t, err)
	assert.Zero(t, countDocs(t, td, captures))
}

// A second attempt at a stored height changes nothing. It reports the stored header as the block's
// when the hashes match, even with different evidence, and as another block's when they differ.
func TestStore_TokenModeStoredBlockIsLeftIntact(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		change  func(result chains.ConversionResult)
		wantErr error
	}{
		{
			name: "same header, other evidence",
			change: func(result chains.ConversionResult) {
				evidence := maps.Clone(result.Groups[1].Docs[0])
				evidence[evm.RecordsFieldName] = "00"
				result.Groups[1].Docs[0] = evidence
			},
			wantErr: ErrHeaderStored,
		},
		{
			name: "other header",
			change: func(result chains.ConversionResult) {
				header := maps.Clone(result.Groups[0].Docs[0])
				header[constants.HashFieldName] = deterministicHash("other")
				result.Groups[0].Docs[0] = header
			},
			wantErr: ErrStoredHeaderDiffers,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			td, conv := tokenNode(t)
			storeTokenBlock(t, td, conv)

			again := convertTokenBlock(t, conv, tokenCapture)
			tc.change(again)
			handler, err := NewBlockHandler(td.Node, 1000)
			require.NoError(t, err)
			_, err = handler.Store(ctxWithIdentity(t), again)
			require.ErrorIs(t, err, tc.wantErr)
			for _, name := range conv.GetCollections() {
				assert.Equal(t, 1, countDocs(t, td, name), name)
			}
		})
	}
}
