package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// maxStateSlots is the most storage slots one state request may name. geth refuses an eth_getProof
// call with more storage keys (maxGetProofKeys in its internal/ethapi package), so a larger request
// could not be answered by an Ethereum node.
const maxStateSlots = 1024

var (
	blockHashPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	addressPattern   = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	slotPattern      = regexp.MustCompile(`^0x[0-9a-fA-F]{1,64}$`)
)

// StateProof returns the number of the block with hash blockHash and the encoded proof of
// contract's slots at that block.
type StateProof func(ctx context.Context, blockHash, contract string, slots []string) (int64, []byte, error)

type stateRequest struct {
	BlockHash string   `json:"blockHash"`
	Contract  string   `json:"contract"`
	Slots     []string `json:"slots"`
}

type stateResponse struct {
	BlockNumber int64  `json:"blockNumber"`
	BlockHash   string `json:"blockHash"`
	Proof       string `json:"proof"`
}

// EnableStateEndpoint registers the authenticated endpoint that answers a read of a contract's
// storage slots at a block with the node's proof, so a host can check the values against the
// block's verified header.
func (hs *HealthServer) EnableStateEndpoint(prove StateProof, auth Authenticator) {
	hs.mux.HandleFunc("POST /api/v1/state", authMiddleware(auth, stateHandler(prove), slog.Default()))
}

// stateHandler answers {blockHash, contract, slots} with {blockNumber, blockHash, proof}, the proof
// in 0x-prefixed hex. A malformed request is refused without asking the node; a node error is
// answered with 502 and the node's message.
func stateHandler(prove StateProof) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req stateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !validStateRequest(req) {
			writeJSONError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf(
				"the body must be {blockHash, contract, slots}: a 0x block hash, a 0x contract address and 1 to %d 0x slots", maxStateSlots))
			return
		}
		number, proof, err := prove(r.Context(), req.BlockHash, req.Contract, req.Slots)
		if err != nil {
			logger.Sugar.Warnf("Cannot prove %d slots of %s at block %s: %v", len(req.Slots), req.Contract, req.BlockHash, err)
			writeJSONError(w, http.StatusBadGateway, "node_error", err.Error())
			return
		}
		w.Header().Set("Content-Type", constants.ContentTypeJSON)
		_ = json.NewEncoder(w).Encode(stateResponse{BlockNumber: number, BlockHash: req.BlockHash, Proof: "0x" + hex.EncodeToString(proof)})
	}
}

// validStateRequest reports whether req names a block hash, a contract address and 1 to
// maxStateSlots slots, each in 0x-prefixed hex.
func validStateRequest(req stateRequest) bool {
	if !blockHashPattern.MatchString(req.BlockHash) || !addressPattern.MatchString(req.Contract) ||
		len(req.Slots) == 0 || len(req.Slots) > maxStateSlots {
		return false
	}
	for _, slot := range req.Slots {
		if !slotPattern.MatchString(slot) {
			return false
		}
	}
	return true
}
