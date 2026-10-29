package tron

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// fakeTronClient is a lightweight tronClient fake: each fetch surface is one
// settable value (result or error), consumed once per test.
type fakeTronClient struct {
	blockByNum    *Block
	blockByNumErr error

	receipts    []*TransactionInfo
	receiptsErr error

	blockByID    *Block
	blockByIDErr error

	nowBlock      *Block
	nowBlockErr   error
	solidityBlock *Block
	solidityErr   error

	closeCalled bool
}

func (f *fakeTronClient) GetBlockByNum(_ context.Context, _ int64) (*Block, error) {
	return f.blockByNum, f.blockByNumErr
}

func (f *fakeTronClient) GetBlockByID(_ context.Context, _ string) (*Block, error) {
	return f.blockByID, f.blockByIDErr
}

func (f *fakeTronClient) GetNowBlock(_ context.Context) (*Block, error) {
	return f.nowBlock, f.nowBlockErr
}

func (f *fakeTronClient) GetSolidifiedNowBlock(_ context.Context) (*Block, error) {
	return f.solidityBlock, f.solidityErr
}

func (f *fakeTronClient) GetTransactionInfoByBlockNum(_ context.Context, _ int64) ([]*TransactionInfo, error) {
	return f.receipts, f.receiptsErr
}

func (f *fakeTronClient) Close() error {
	f.closeCalled = true
	return nil
}

// walletFakeHandler answers one wallet-API request with a status and body,
// keyed by the request path, the received apiKey header, and the request
// body; handlers assert request shapes inside the httptest round trip.
type walletFakeHandler func(path, apiKey string, body []byte) (status int, resp []byte)

// newWalletFake serves a wallet-API-shaped fake: every request is a POST
// that completes only when the handler has read the full body.
func newWalletFake(t *testing.T, handler walletFakeHandler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		status, resp := handler(r.URL.Path, r.Header.Get("Tron-Pro-Api-Key"), body)
		w.WriteHeader(status)
		_, _ = w.Write(resp)
	}))
	t.Cleanup(server.Close)
	return server
}

// ---------------------------------------------------------------------------
// DefraDB-backed helpers for the converter progress-query tests
// ---------------------------------------------------------------------------

// tronTestConfig builds a minimal Tron config for DefraDB-backed tests: the
// chain section drives the Tron__Mainnet collection prefix, and the wallet
// and indexer sections stay zero because these tests exercise only the
// converter's DefraDB query layer.
func tronTestConfig() *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Mainnet"},
	}
}

// fakeTronBlockID derives a deterministic 64-hex blockID, the wire style of
// java-tron: sha256 of the header content, rendered without the EVM "0x"
// prefix. Distinct inputs keep content-derived DocIDs distinct.
func fakeTronBlockID(num int64) string {
	sum := sha256.Sum256([]byte("tron-block-" + strconv.FormatInt(num, 10)))
	return hex.EncodeToString(sum[:])
}

// fakeTronBlock builds a complete block the way the wallet API serves one
// (witness signature nested inside block_header), populated far enough to
// satisfy every field the block document builder reads.
func fakeTronBlock(num int64) *Block {
	return &Block{
		BlockID: fakeTronBlockID(num),
		BlockHeader: BlockHeader{
			RawData: BlockHeaderRawData{
				Number:         num,
				Timestamp:      1777445121000 + num,
				ParentHash:     fakeTronBlockID(num - 1),
				WitnessAddress: "41920E5498B1E7066CA2944B4B5D8BCE6CB9BE9E76",
				Version:        3,
				TxTrieRoot:     fakeTronBlockID(num + 90000),
			},
			WitnessSignature: "fake-witness-signature-" + strconv.FormatInt(num, 10),
		},
	}
}

// setupTronDefraDB starts an in-memory DefraDB preloaded with the tron
// schema. The plain SetupTestDefraDB applies the EVM default schema, so the
// tron tests must hand the converter's own merged SDL to the with-schema
// variant instead.
func setupTronDefraDB(t *testing.T, c *Converter) *testutils.TestDefraDB {
	t.Helper()

	sdl, err := c.GetSchema()
	require.NoError(t, err)
	return testutils.SetupTestDefraDBWithSchema(t, sdl)
}

// storeTestBlockDoc writes a single block document through the same write
// path used in production (buildBlockData -> NewDocFromMap -> AddDocument).
func storeTestBlockDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, num int64) string {
	t.Helper()

	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)

	col, err := txn.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)

	doc, err := client.NewDocFromMap(ctx, buildBlockData(fakeTronBlock(num)), col.Version())
	require.NoError(t, err)

	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())

	return doc.ID().String()
}

// storeResidueBlockDoc writes a block document without a number field,
// simulating the numberless rows left behind by purge residue or P2P
// replication that the block-number queries must exclude. The seed keeps
// the content unique: DefraDB derives DocIDs from document content, so
// identical residue documents would collide on the same DocID.
func storeResidueBlockDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, seed int64) string {
	t.Helper()

	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)

	col, err := txn.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)

	data := buildBlockData(fakeTronBlock(seed))
	delete(data, constants.NumberFieldName)

	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	require.NoError(t, err)

	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())

	return doc.ID().String()
}
