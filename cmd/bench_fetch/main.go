// Command bench_fetch captures raw Solana getBlock payloads into the JSON
// fixture the replay benchmark consumes (benchmarking/solana/replay_bench_test.go).
//
// The request shape mirrors the production client (Client.getBlock): base64
// encoding, full transaction details, confirmed commitment, rewards, and
// maxSupportedTransactionVersion=1. Responses are POSTed by hand instead of
// going through the SDK so the node's bytes land verbatim in the fixture —
// the replay server serves exactly what a real node served at capture time.
//
// Environment: SOLANA_RPC_URL (required), optional SOLANA_API_KEY and
// SOLANA_API_KEY_TYPE (defaults to x-api-key). Loaded from .env when present.
// Keys and the endpoint URL are never written into the fixture.
//
// Usage: go run ./cmd/bench_fetch --from 449791000 --to 449791099
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	retryMaxAttempts = 5
	retryBackoff     = 500 * time.Millisecond
	retryMaxWait     = 30 * time.Second
	requestTimeout   = 60 * time.Second
)

// rpcErrorPayload mirrors the JSON-RPC error object; the fixture stores it
// verbatim so the replay can reproduce skip-classification behaviour
// (-32004/-32007/-32009 map to distinct client sentinels).
type rpcErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcErrorPayload) Error() string {
	return fmt.Sprintf("code %d: %s", e.Code, e.Message)
}

type rpcResponse struct {
	Result json.RawMessage  `json:"result"`
	Error  *rpcErrorPayload `json:"error"`
}

// slotEntry is one fixture block: either a raw getBlock result payload or a
// null-result/skipped-or-error outcome. nil Result marshals as null.
type slotEntry struct {
	Slot   uint64           `json:"slot"`
	Result json.RawMessage  `json:"result"`
	Error  *rpcErrorPayload `json:"error,omitempty"`
}

type fixtureMeta struct {
	StartSlot  uint64      `json:"startSlot"`
	EndSlot    uint64      `json:"endSlot"`
	Commitment string      `json:"commitment"`
	Encoding   string      `json:"encoding"`
	FetchedAt  time.Time   `json:"fetchedAt"`
	Blocks     []slotEntry `json:"blocks"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "bench_fetch: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Interrupt-aware so a partially captured range can still be saved.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	from := flag.Uint64("from", 449791000, "first slot to capture")
	to := flag.Uint64("to", 449791099, "last slot to capture (inclusive)")
	out := flag.String("out", "",
		"fixture path (default benchmarking/solana/testdata/bench_replay_<from>_<to>.json)")
	delay := flag.Duration("delay", 250*time.Millisecond, "wait between getBlock requests")
	commitment := flag.String("commitment", "confirmed", "getBlock commitment")
	flag.Parse()

	if *from > *to {
		return fmt.Errorf("--from %d must not exceed --to %d", *from, *to)
	}
	outPath := *out
	if outPath == "" {
		outPath = fmt.Sprintf("benchmarking/solana/testdata/bench_replay_%d_%d.json", *from, *to)
	}

	// Tolerated failure: when the tool runs through the Makefile the .env
	// values arrive via the environment already.
	_ = godotenv.Load(".env")
	rpcURL := strings.TrimSpace(os.Getenv("SOLANA_RPC_URL"))
	if rpcURL == "" {
		return fmt.Errorf("SOLANA_RPC_URL is empty; set it in .env or the environment")
	}
	apiKey := os.Getenv("SOLANA_API_KEY")
	keyType := strings.TrimSpace(os.Getenv("SOLANA_API_KEY_TYPE"))
	if keyType == "" {
		keyType = "x-api-key"
	}

	client := &http.Client{Timeout: requestTimeout}
	meta := fixtureMeta{
		StartSlot:  *from,
		EndSlot:    *to,
		Commitment: *commitment,
		Encoding:   "base64",
		FetchedAt:  time.Now().UTC(),
	}

	fetched, nulled, errored, totalBytes := 0, 0, 0, 0
	interrupted := false
	for slot := *from; slot <= *to; slot++ {
		if ctx.Err() != nil {
			interrupted = true
			break
		}
		entry, size := fetchSlot(ctx, client, rpcURL, apiKey, keyType, slot, *commitment)
		meta.Blocks = append(meta.Blocks, entry)
		totalBytes += size
		switch {
		case entry.Error != nil:
			errored++
			fmt.Fprintf(os.Stderr, "slot %d: RPC error %s (%d bytes)\n", slot, entry.Error, size)
		case len(entry.Result) == 0:
			nulled++
			fmt.Fprintf(os.Stderr, "slot %d: null result (skipped or unknown) (%d bytes)\n", slot, size)
		default:
			fetched++
			fmt.Fprintf(os.Stderr, "slot %d: captured (%d bytes)\n", slot, size)
		}
		// Sleep between requests, but never after the last one, and bail out
		// promptly when Ctrl-C arrives mid-wait.
		if slot != *to {
			select {
			case <-ctx.Done():
				interrupted = true
			case <-time.After(*delay):
			}
			if interrupted {
				break
			}
		}
	}

	if err := save(meta, outPath); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\nfetched: %d   null: %d   error: %d   total: %s\n",
		fetched, nulled, errored, byteCount(totalBytes))
	if interrupted {
		fmt.Fprintf(os.Stderr, "interrupted — partial capture saved up to slot %d\n", lastSlot(meta))
	}
	if fetched == 0 && !interrupted {
		fmt.Fprintf(os.Stderr, "⚠️  no block was captured: the range may lie beyond the node's tip "+
			"(%s commitment), the slots were skipped, or the endpoint rejected every request — "+
			"the replay benchmark would have nothing to process\n", *commitment)
	}
	fmt.Fprintf(os.Stderr, "fixture written: %s\n", outPath)
	return nil
}

// fetchSlot issues one getBlock with production-faithful parameters and
// retries transport failures (network errors, 429/503) with Retry-After /
// exponential backoff, mirroring the client's retryTransport bounds.
// Retries never surface as errors: exhaustion degrades into an error entry
// so one flaky slot cannot abort the whole capture.
func fetchSlot(
	ctx context.Context,
	client *http.Client,
	url, apiKey, keyType string,
	slot uint64,
	commitment string,
) (slotEntry, int) {
	// Parameter shape identical to Client.getBlock (encoding, full details,
	// commit+rewards, mandatory maxSupportedTransactionVersion).
	opts := map[string]any{
		"encoding":                       "base64",
		"transactionDetails":             "full",
		"rewards":                        true,
		"commitment":                     commitment,
		"maxSupportedTransactionVersion": 1,
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "getBlock",
		"params": []any{slot, opts},
	})
	if err != nil {
		return slotEntry{Slot: slot, Error: &rpcErrorPayload{Code: -1, Message: err.Error()}}, 0
	}

	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return slotEntry{Slot: slot, Error: &rpcErrorPayload{Code: -1, Message: err.Error()}}, 0
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set(keyType, apiKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return slotEntry{Slot: slot, Error: &rpcErrorPayload{Code: -1, Message: ctx.Err().Error()}}, 0
			}
			if attempt >= retryMaxAttempts {
				return slotEntry{Slot: slot, Error: &rpcErrorPayload{Code: -1, Message: err.Error()}}, 0
			}
			waitOut(backoff(attempt, ""))
			continue
		}

		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			if ctx.Err() != nil {
				return slotEntry{Slot: slot, Error: &rpcErrorPayload{Code: -1, Message: ctx.Err().Error()}}, len(raw)
			}
			if attempt >= retryMaxAttempts {
				return slotEntry{Slot: slot, Error: &rpcErrorPayload{Code: -1, Message: readErr.Error()}}, len(raw)
			}
			waitOut(backoff(attempt, ""))
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			if attempt >= retryMaxAttempts {
				return slotEntry{Slot: slot, Error: &rpcErrorPayload{
					Code: resp.StatusCode, Message: fmt.Sprintf("HTTP %d after %d attempts", resp.StatusCode, attempt),
				}}, len(raw)
			}
			waitOut(backoff(attempt, resp.Header.Get("Retry-After")))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return slotEntry{Slot: slot, Error: &rpcErrorPayload{
				Code: resp.StatusCode, Message: fmt.Sprintf("unexpected HTTP %d: %.200s", resp.StatusCode, raw),
			}}, len(raw)
		}

		var envelope rpcResponse
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return slotEntry{Slot: slot, Error: &rpcErrorPayload{Code: -1, Message: err.Error()}}, len(raw)
		}
		if envelope.Error != nil {
			return slotEntry{Slot: slot, Error: envelope.Error}, len(raw)
		}
		// null result → skipped-or-unknown slot; stored as an explicit null.
		if bytes.Equal(bytes.TrimSpace(envelope.Result), []byte("null")) {
			return slotEntry{Slot: slot}, len(raw)
		}
		return slotEntry{Slot: slot, Result: envelope.Result}, len(raw)
	}
}

// backoff computes the wait for one retry attempt: Retry-After (seconds or
// HTTP-date) when parseable, otherwise exponential from attempt, clamped to
// [retryBackoff, retryMaxWait] like the client transport.
func backoff(attempt int, retryAfter string) time.Duration {
	wait := retryBackoff << (attempt - 1)
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs >= 0 {
		wait = time.Duration(secs) * time.Second
	}
	if wait < retryBackoff {
		wait = retryBackoff
	}
	if wait > retryMaxWait {
		wait = retryMaxWait
	}
	return wait
}

// waitOut sleeps for one retry delay. Context wake-up is not wired here:
// the next loop iteration's request fails fast on the cancelled context.
func waitOut(d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	<-timer.C
}

// lastSlot reports the last slot appended to the fixture (for interrupt
// messages); 0 when nothing was captured.
func lastSlot(meta fixtureMeta) uint64 {
	if len(meta.Blocks) == 0 {
		return 0
	}
	return meta.Blocks[len(meta.Blocks)-1].Slot
}

func byteCount(n int) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// save writes the fixture compactly and atomically: a partial write must
// never masquerade as a complete capture for the benchmark. The destination
// directory is created on demand so a fresh checkout can capture before any
// testdata folder exists.
func save(meta fixtureMeta, outPath string) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal fixture: %w", err)
	}
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create fixture directory %s: %w", dir, err)
	}
	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, outPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename fixture into place: %w", err)
	}
	return nil
}
