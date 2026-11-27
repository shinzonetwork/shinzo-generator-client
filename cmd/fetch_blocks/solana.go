package main

// The solana capture backend speaks Solana JSON-RPC over HTTP. It mirrors the
// production client's getBlock call shape (pkg/chains/solana/solana_client.go)
// so the captured fixture bytes decode identically through the SDK when the
// replay mock serves them verbatim. Unlike the Tron wallet API there is no
// separate receipts endpoint: the full-fidelity getBlock response already
// embeds every transaction's metadata (logs, inner instructions, token
// balances) plus the block rewards, so one slot is one call. The fixture
// holds only the slots the chain filled: a leader skip ends its slot with
// no entry, so gaps between entries are chain skips the replay side sees
// as the mock's null getBlock answers.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

const (
	// receiptModeEmbedded labels the Solana receipt capture: the getBlock
	// response embeds the receipts-equivalent metadata, so the fixture's
	// Receipts field stays null and replay consumers need no second call.
	receiptModeEmbedded = "embedded"

	// The getBlock config production sends on every call. Commitment rides
	// from SOLANA_COMMITMENT (default confirmed, the shipped
	// config_solana.yaml value), and the version ceiling matters: blocks
	// containing versioned (v0+) transactions are rejected without it.
	solanaCaptureEncoding     = "base64"
	solanaCaptureTxDetails    = "full"
	solanaCaptureMaxTxVersion = 1
	solanaDefaultCommitment   = "confirmed"

	// solanaCaptureAttempts bounds one slot's capture: 1 try plus 3 retries
	// waiting 500ms, 1s, 2s. Near the tip a load-balanced endpoint can route
	// a call to a node that has not indexed the slot yet; the bounded wait
	// usually outlasts that lag, while a permanently missing slot still
	// records within seconds.
	solanaCaptureAttempts = 4

	// Solana JSON-RPC server error codes that classify a missing-slot
	// outcome, mirrored from pkg/chains/solana/solana_client.go (unexported
	// there) so this capture CLI stays a dependency-light main package.
	solanaCodeBlockCleanedUp       = -32001 // pruned below the node's ledger floor
	solanaCodeBlockNotAvailable    = -32004 // absent from the node's blockstore
	solanaCodeSlotSkipped          = -32007 // skipped by its leader or a ledger jump
	solanaCodeLTStorageSlotSkipped = -32009 // absent from the node's long-term storage
	solanaCodeUnsupportedTxVersion = -32015 // versioned txs with an absent version ceiling
)

// solanaBackend captures replay fixtures over Solana JSON-RPC. It reuses the
// shared rpcEnv transport (headers, status handling, retries) and only owns
// the call shapes and the missing-slot classification.
type solanaBackend struct {
	env *rpcEnv
	// commitment is the visibility bound sent on getSlot and getBlock, read
	// from SOLANA_COMMITMENT so a capture can mirror a finalized-mode
	// production run.
	commitment string
}

// newSolanaBackend reads the Solana endpoint env. Every value is normalized
// the way the production client's authHeader does: endpoint and key trimmed
// (a stray space on an .env line would otherwise surface deep inside the
// transport as an invalid header name or an unparseable URL, after the
// retry budget burned), and the header name trimmed and lower-cased. An
// empty SOLANA_API_KEY_TYPE keeps the customary x-api-key, and a key
// without a header name is unusable anyway.
func newSolanaBackend(opts *captureOptions) (*solanaBackend, error) {
	rpcURL := strings.TrimSpace(os.Getenv("SOLANA_RPC_URL"))
	if rpcURL == "" {
		return nil, fmt.Errorf("SOLANA_RPC_URL is required: point it at a JSON-RPC endpoint for %s %s", opts.chain, opts.network)
	}
	apiKey := strings.TrimSpace(os.Getenv("SOLANA_API_KEY"))
	apiKeyType := strings.ToLower(strings.TrimSpace(os.Getenv("SOLANA_API_KEY_TYPE")))
	if apiKey != "" && apiKeyType == "" {
		apiKeyType = "x-api-key"
	}
	commitment := strings.TrimSpace(os.Getenv("SOLANA_COMMITMENT"))
	if commitment == "" {
		commitment = solanaDefaultCommitment
	}
	return &solanaBackend{
		env: &rpcEnv{
			client:     &http.Client{Timeout: httpTimeout},
			url:        rpcURL,
			apiKey:     apiKey,
			apiKeyType: apiKeyType,
		},
		commitment: commitment,
	}, nil
}

func (b *solanaBackend) redactedEndpoint() string {
	return utils.RedactEndpoint(b.env.url)
}

// fetchTip resolves the current tip via getSlot at the configured
// commitment. On Solana the slot is the fetch height, and the answer is a
// plain JSON number.
func (b *solanaBackend) fetchTip(ctx context.Context) (uint64, error) {
	raw, err := b.call(ctx, "getSlot", []any{map[string]string{"commitment": b.commitment}})
	if err != nil {
		return 0, fmt.Errorf("getSlot: %w", err)
	}
	var slot uint64
	if err := json.Unmarshal(raw, &slot); err != nil {
		return 0, fmt.Errorf("parse getSlot result %s: %w", raw, err)
	}
	return slot, nil
}

// captureBlock fetches one slot's block, keeping the node's result bytes
// verbatim. The fixture holds only the slots the chain actually filled: a
// skipped slot answers the errBlockSkipped sentinel and the walk simply
// moves on, the gaps between entries being chain skips. A missing slot is
// a business answer, not a transport failure, and the spellings differ in
// permanence: the affirmative skip codes (-32007, -32009) end the slot
// immediately, while the ambiguous ones (null result, -32004) retry a
// bounded few times first, the same near-tip lag treatment the Tron
// capture applies. When the retries cannot clear an ambiguous spelling the
// slot ends with the sentinel rather than failing the walk: below the tip
// a slot with no block is permanent (the same classification the
// production fetcher draws), and a walk that hard-stopped on the first
// skip could not produce fixtures on endpoints that spell skips as -32004.
func (b *solanaBackend) captureBlock(ctx context.Context, num uint64) (*fixtureBlock, error) {
	for attempt := range solanaCaptureAttempts {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryBackoffFn(attempt)); err != nil {
				return nil, err
			}
		}
		fb, err := b.captureBlockOnce(ctx, num)
		if err == nil {
			return fb, nil
		}
		if _, ok := errors.AsType[*slotLagError](err); !ok {
			return nil, err
		}
		log.Printf("slot %d: %v (attempt %d/%d, the answering node may still be indexing the slot)", num, err, attempt+1, solanaCaptureAttempts)
	}
	log.Printf("slot %d: still not visible after %d attempts, ending the slot without an entry", num, solanaCaptureAttempts)
	return nil, errBlockSkipped
}

// slotLagError marks a capture answer that means either "the answering node
// has not indexed this slot yet" or "the slot was skipped and this node
// spells skips this way": a null result or the -32004 not-available code.
// The affirmative skip codes never carry it, so only these ambiguous
// spellings spend the lag retries. It is not a transport failure
// (withRetries retries only transport errors and 429/5xx), which is why the
// backend retries it one level up, the same structure the Tron capture uses
// for its indexLagError.
type slotLagError struct {
	err error
}

func (e *slotLagError) Error() string { return e.err.Error() }

func (e *slotLagError) Unwrap() error { return e.err }

// captureBlockOnce performs one block fetch attempt, keeping the node's
// result bytes verbatim. Affirmative skip answers end the slot with the
// errBlockSkipped sentinel; the ambiguous spellings return a slotLagError
// for the retry loop above; parse errors, the ledger floor, and any other
// code stay fatal so a retry cannot mask them.
func (b *solanaBackend) captureBlockOnce(ctx context.Context, num uint64) (*fixtureBlock, error) {
	blockRaw, err := b.call(ctx, "getBlock", []any{num, map[string]any{
		"encoding":                       solanaCaptureEncoding,
		"transactionDetails":             solanaCaptureTxDetails,
		"commitment":                     b.commitment,
		"rewards":                        true,
		"maxSupportedTransactionVersion": solanaCaptureMaxTxVersion,
	}})
	if err != nil {
		if envErr, ok := errors.AsType[*rpcEnvelopeError](err); ok {
			switch envErr.code {
			case solanaCodeSlotSkipped, solanaCodeLTStorageSlotSkipped:
				return nil, errBlockSkipped
			case solanaCodeBlockNotAvailable:
				return nil, &slotLagError{err: fmt.Errorf("slot %d not available on the node (rpc error %d: %s)", num, envErr.code, envErr.message)}
			case solanaCodeBlockCleanedUp:
				return nil, fmt.Errorf("slot %d was pruned below the endpoint's ledger floor (rpc error %d: %s): capture a range the endpoint still holds", num, envErr.code, envErr.message)
			case solanaCodeUnsupportedTxVersion:
				return nil, fmt.Errorf("slot %d: getBlock rejected the version ceiling (rpc error %d: %s)", num, envErr.code, envErr.message)
			}
		}
		return nil, err
	}
	if isJSONNull(blockRaw) {
		return nil, &slotLagError{err: fmt.Errorf("slot %d not visible on the node (null result)", num)}
	}
	// A produced block always carries a blockhash; a present-but-empty
	// object means a degenerate gateway answer, not a block.
	var block struct {
		Blockhash string `json:"blockhash"`
	}
	if err := json.Unmarshal(blockRaw, &block); err != nil {
		return nil, fmt.Errorf("parse getBlock result for slot %d: %w", num, err)
	}
	if block.Blockhash == "" {
		return nil, fmt.Errorf("slot %d not available on the node (missing blockhash)", num)
	}

	return &fixtureBlock{
		Number:      strconv.FormatUint(num, 10),
		Block:       blockRaw,
		Receipts:    json.RawMessage("null"),
		ReceiptMode: receiptModeEmbedded,
	}, nil
}

// call issues one Solana JSON-RPC request through the shared retry loop,
// reusing the transport's backoff, Retry-After handling, and interrupt
// awareness unchanged. The typed envelope error keeps the missing-slot
// classification code-driven.
func (b *solanaBackend) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return withRetries(ctx, method, func() (json.RawMessage, error) {
		return rpcOnce(ctx, b.env, method, params)
	})
}
