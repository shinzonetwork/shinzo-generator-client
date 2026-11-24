package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

const (
	// maxStateSlots is the most storage slots one state request may name. geth refuses an
	// eth_getProof call with more storage keys (maxGetProofKeys in its internal/ethapi package), so a
	// larger request could not be answered by an Ethereum node.
	maxStateSlots = 1024
	// maxStateRequestBytes caps a state request's body. The largest valid request, maxStateSlots
	// slots of 64 hex digits, is 70,803 bytes as compact JSON.
	maxStateRequestBytes = 128 << 10
	// stateTimeout bounds the node calls behind one state request.
	stateTimeout = 10 * time.Second
	// maxConcurrentStateRequests is how many state requests are answered at once. Each one is two
	// calls on the operator's RPC, so more are refused rather than queued.
	maxConcurrentStateRequests = 4
)

var (
	blockHashPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	addressPattern   = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	slotPattern      = regexp.MustCompile(`^0x[0-9a-fA-F]{1,64}$`)
)

// StateProof returns the number of the block with hash blockHash and the encoded proof of the
// account at address, and of its slots, at that block.
type StateProof func(ctx context.Context, blockHash, address string, slots []string) (int64, []byte, error)

type stateRequest struct {
	BlockHash string   `json:"blockHash"`
	Address   string   `json:"address"`
	Slots     []string `json:"slots"`
}

type stateResponse struct {
	BlockNumber int64  `json:"blockNumber"`
	BlockHash   string `json:"blockHash"`
	Proof       string `json:"proof"`
}

// EnableStateEndpoint registers the authenticated endpoint that answers a read of an account and
// its storage slots at a block with the node's proof, so a host can check the values against the
// block's verified header.
func (hs *HealthServer) EnableStateEndpoint(prove StateProof, auth Authenticator) {
	hs.mux.HandleFunc("POST /api/v1/state", authMiddleware(auth, stateHandler(prove), slog.Default()))
}

// stateHandler answers {blockHash, address, slots} with {blockNumber, blockHash, proof}, the proof
// in 0x-prefixed hex. A malformed request is refused without asking the node, and one arriving while
// maxConcurrentStateRequests are being answered is refused with 503. A node error, or no answer
// within stateTimeout, is answered with 502 and the error's message.
func stateHandler(prove StateProof) http.HandlerFunc {
	answering := make(chan struct{}, maxConcurrentStateRequests)
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxStateRequestBytes)
		var req stateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !validStateRequest(req) {
			writeJSONError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf(
				"the body must be {blockHash, address, slots}: a 0x block hash, a 0x address and up to %d 0x slots", maxStateSlots))
			return
		}
		select {
		case answering <- struct{}{}:
			defer func() { <-answering }()
		default:
			writeJSONError(w, http.StatusServiceUnavailable, "busy", "too many state requests at once, retry later")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), stateTimeout)
		defer cancel()
		number, proof, err := prove(ctx, req.BlockHash, req.Address, req.Slots)
		if err != nil {
			logger.Sugar.Warnf("Cannot prove %s with %d slots at block %s: %v", req.Address, len(req.Slots), req.BlockHash, err)
			writeJSONError(w, http.StatusBadGateway, "node_error", err.Error())
			return
		}
		w.Header().Set("Content-Type", constants.ContentTypeJSON)
		_ = json.NewEncoder(w).Encode(stateResponse{BlockNumber: number, BlockHash: req.BlockHash, Proof: "0x" + hex.EncodeToString(proof)})
	}
}

// validStateRequest reports whether req names a block hash, an address and up to maxStateSlots
// slots, each in 0x-prefixed hex.
func validStateRequest(req stateRequest) bool {
	if !blockHashPattern.MatchString(req.BlockHash) || !addressPattern.MatchString(req.Address) ||
		len(req.Slots) > maxStateSlots {
		return false
	}
	for _, slot := range req.Slots {
		if !slotPattern.MatchString(slot) {
			return false
		}
	}
	return true
}
