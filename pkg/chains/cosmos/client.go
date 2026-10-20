package cosmos

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxResponseBytes bounds a single CometBFT/REST response body. Blocks with
// large IBC client updates stay well below it.
const maxResponseBytes = 64 << 20

// Client reads the native Cosmos view of a block from a CometBFT RPC
// endpoint (/block_results: execution results and events) and the Cosmos
// REST API (GetBlockWithTxs: tx bytes and JSON-decoded messages). Decoding
// via REST keeps the generator free of the chain's protobuf registry.
type Client struct {
	rpcURL string
	lcdURL string
	http   *http.Client
}

// NewClient creates a Client. A positive timeout bounds each request.
func NewClient(rpcURL, lcdURL string, timeout time.Duration) *Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	return &Client{
		rpcURL: strings.TrimRight(rpcURL, "/"),
		lcdURL: strings.TrimRight(lcdURL, "/"),
		http:   httpClient,
	}
}

// FetchBlock fetches and assembles the Cosmos view of the block at height.
// Both requests run concurrently; the tx counts of the two sources must agree.
func (c *Client) FetchBlock(ctx context.Context, height int64) (*Block, error) {
	var (
		wg             sync.WaitGroup
		lcd            lcdBlockWithTxs
		results        *blockResults
		lcdErr, rpcErr error
	)
	wg.Add(2) //nolint:mnd // two concurrent requests
	go func() {
		defer wg.Done()
		lcdErr = c.getJSON(ctx, fmt.Sprintf("%s/cosmos/tx/v1beta1/txs/block/%d?pagination.limit=10000", c.lcdURL, height), &lcd)
	}()
	go func() {
		defer wg.Done()
		var resp rpcResponse[blockResults]
		rpcErr = c.getJSON(ctx, fmt.Sprintf("%s/block_results?height=%d", c.rpcURL, height), &resp)
		if rpcErr == nil {
			switch {
			case resp.Error != nil:
				rpcErr = fmt.Errorf("block_results: %s: %s", resp.Error.Message, resp.Error.Data) //nolint:err113
			case resp.Result == nil:
				rpcErr = fmt.Errorf("block_results: empty result") //nolint:err113
			default:
				results = resp.Result
			}
		}
	}()
	wg.Wait()
	if lcdErr != nil {
		return nil, fmt.Errorf("cosmos block %d: %w", height, lcdErr)
	}
	if rpcErr != nil {
		return nil, fmt.Errorf("cosmos block %d: %w", height, rpcErr)
	}
	return assembleBlock(height, &lcd, results)
}

// getJSON GETs url and decodes the JSON body into out.
func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d: %s", req.URL.Path, resp.StatusCode, truncate(string(body), 200)) //nolint:err113
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", req.URL.Path, err)
	}
	return nil
}

// assembleBlock joins the REST and CometBFT views of one block. Transactions
// are matched by position, which both sources report in block order.
func assembleBlock(height int64, lcd *lcdBlockWithTxs, results *blockResults) (*Block, error) {
	if h, err := strconv.ParseInt(lcd.Block.Header.Height, 10, 64); err != nil || h != height {
		return nil, fmt.Errorf("cosmos block %d: REST returned height %q", height, lcd.Block.Header.Height) //nolint:err113
	}
	if h, err := strconv.ParseInt(results.Height, 10, 64); err != nil || h != height {
		return nil, fmt.Errorf("cosmos block %d: block_results returned height %q", height, results.Height) //nolint:err113
	}
	if lcd.Pagination != nil && lcd.Pagination.NextKey != nil && *lcd.Pagination.NextKey != "" {
		return nil, fmt.Errorf("cosmos block %d: REST tx list is paginated", height) //nolint:err113
	}
	rawTxs := lcd.Block.Data.Txs
	if len(lcd.Txs) != len(rawTxs) || len(results.TxsResults) != len(rawTxs) {
		return nil, fmt.Errorf("cosmos block %d: tx count mismatch: %d tx bytes, %d decoded txs, %d results", //nolint:err113
			height, len(rawTxs), len(lcd.Txs), len(results.TxsResults))
	}

	hashBytes, err := base64.StdEncoding.DecodeString(lcd.BlockID.Hash)
	if err != nil {
		return nil, fmt.Errorf("cosmos block %d: decode block hash: %w", height, err)
	}

	block := &Block{
		Height:         height,
		Hash:           "0x" + hex.EncodeToString(hashBytes),
		Transactions:   make([]Transaction, len(rawTxs)),
		FinalizeEvents: convertEvents(results.FinalizeBlockEvents),
	}
	for i, rawTx := range rawTxs {
		txBytes, err := base64.StdEncoding.DecodeString(rawTx)
		if err != nil {
			return nil, fmt.Errorf("cosmos block %d: decode tx %d: %w", height, i, err)
		}
		sum := sha256.Sum256(txBytes)
		result := results.TxsResults[i]
		decoded := lcd.Txs[i]

		messages := make([]Message, len(decoded.Body.Messages))
		for j, msg := range decoded.Body.Messages {
			var probe struct {
				Type string `json:"@type"`
			}
			if err := json.Unmarshal(msg, &probe); err != nil {
				return nil, fmt.Errorf("cosmos block %d: tx %d message %d: %w", height, i, j, err)
			}
			messages[j] = Message{Index: j, TypeURL: probe.Type, Body: string(msg)}
		}

		block.Transactions[i] = Transaction{
			Hash:      strings.ToUpper(hex.EncodeToString(sum[:])),
			Index:     i,
			Code:      result.Code,
			Codespace: result.Codespace,
			Log:       result.Log,
			GasWanted: result.GasWanted,
			GasUsed:   result.GasUsed,
			Memo:      decoded.Body.Memo,
			Fee:       string(decoded.AuthInfo.Fee),
			Messages:  messages,
			Events:    convertEvents(result.Events),
		}
	}
	return block, nil
}

// convertEvents keeps each event's attributes as their raw JSON array.
func convertEvents(events []abciEvent) []Event {
	out := make([]Event, len(events))
	for i, ev := range events {
		attributes := ev.Attributes
		if attributes == nil {
			attributes = []json.RawMessage{}
		}
		attrs, _ := json.Marshal(attributes) // re-marshal of already-valid JSON cannot fail
		out[i] = Event{Index: i, Type: ev.Type, Attributes: string(attrs)}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
