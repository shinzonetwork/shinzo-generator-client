package pruner

import (
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

const (
	uuidSize                    = 16
	uuidHexWithoutHyphensLen    = 32 // hex-encoded uuidSize bytes
	indexerQueueEntriesPrealloc = 128
	indexerQueueGobVersion      = 1
	//
	docIDPrefix = "bae"
)

// BlockEntry holds all document IDs created for a single block.
type BlockEntry struct {
	BlockNumber int64
	BlockDocID  [uuidSize]byte    // single UUID for the block document
	OtherDocIDs map[string][]byte // collectionName → packed UUIDs (len/16 = count)
	BatchSigID  [uuidSize]byte    // single UUID for batch signature
	HasBatchSig bool
}

// indexerQueueSnapshot is the serializable form of the queue.
type indexerQueueSnapshot struct {
	Version     int
	DocIDPrefix string
	Entries     []BlockEntry
}

// IndexerQueue is an in-memory ordered queue of indexed blocks with compact UUID storage.
// Used by indexers that know all docIDs at block creation time.
type IndexerQueue struct {
	mu       sync.Mutex
	entries  []BlockEntry
	filePath string
}

// NewIndexerQueue creates a new empty indexer queue.
func NewIndexerQueue() *IndexerQueue {
	return &IndexerQueue{
		entries: make([]BlockEntry, 0, indexerQueueEntriesPrealloc),
	}
}

// LoadFromFile loads queue entries from a gob-encoded file.
// Returns the number of entries loaded. If the file doesn't exist, returns 0.
func (q *IndexerQueue) LoadFromFile(path string) (int, error) {
	q.filePath = path

	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to open queue file: %w", err)
	}
	defer func() {
		err = f.Close()
		if err != nil {
			logger.Sugar.Warnf("Failed to close queue file: %v", err)
		}
	}()

	var snap indexerQueueSnapshot
	if err := gob.NewDecoder(f).Decode(&snap); err != nil {
		logger.Sugar.Warnf("Failed to decode queue file (format changed, starting empty): %v", err)
		return 0, nil
	}

	if snap.Version != indexerQueueGobVersion {
		logger.Sugar.Warnf("Queue file version mismatch (got %d, want %d), starting empty", snap.Version, indexerQueueGobVersion)
		return 0, nil
	}

	q.mu.Lock()
	q.entries = snap.Entries
	count := len(q.entries)
	q.mu.Unlock()

	return count, nil
}

// Save persists the queue to the file path set by LoadFromFile.
// Uses atomic write (temp file + rename) to avoid corruption.
func (q *IndexerQueue) Save() error {
	if q.filePath == "" {
		return nil
	}

	q.mu.Lock()
	snap := indexerQueueSnapshot{
		Version:     indexerQueueGobVersion,
		DocIDPrefix: docIDPrefix,
		Entries:     make([]BlockEntry, len(q.entries)),
	}
	copy(snap.Entries, q.entries)
	q.mu.Unlock()

	if len(snap.Entries) == 0 {
		err := os.Remove(q.filePath)
		if err != nil {
			logger.Sugar.Warnf("Failed to remove queue file: %v", err)
		}
		return nil
	}

	tmpPath := q.filePath + ".tmp"
	f, err := os.Create(filepath.Clean(tmpPath))
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	if err := gob.NewEncoder(f).Encode(snap); err != nil {
		err := f.Close()
		if err != nil {
			logger.Sugar.Warnf("Failed to close temp file: %v", err)
		}
		err = os.Remove(tmpPath)
		if err != nil {
			logger.Sugar.Warnf("Failed to remove temp file: %v", err)
		}
		return fmt.Errorf("failed to encode queue: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, q.filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// TrackBlockDocIDs adds a block's docIDs to the queue.
// blockDocID is the block document ID, otherDocIDs maps collection name → docID list.
// All entries in otherDocIDs are stored — no chain-specific filtering is applied.
func (q *IndexerQueue) TrackBlockDocIDs(blockNumber int64, blockDocID string, otherDocIDs map[string][]string, batchSigID string) error {
	entry := BlockEntry{
		BlockNumber: blockNumber,
	}

	if blockDocID != "" {
		uuid, err := q.extractUUID(blockDocID)
		if err != nil {
			return fmt.Errorf("invalid block docID: %w", err)
		}
		entry.BlockDocID = uuid
	}

	if len(otherDocIDs) > 0 {
		entry.OtherDocIDs = make(map[string][]byte, len(otherDocIDs))
		for colName, docIDs := range otherDocIDs {
			if len(docIDs) == 0 {
				continue
			}
			packed, err := q.packDocIDs(docIDs)
			if err != nil {
				return fmt.Errorf("invalid docID for %s: %w", colName, err)
			}
			if len(packed) > 0 {
				entry.OtherDocIDs[colName] = packed
			}
		}
	}

	if batchSigID != "" {
		uuid, err := q.extractUUID(batchSigID)
		if err != nil {
			return fmt.Errorf("invalid batch sig docID: %w", err)
		}
		entry.BatchSigID = uuid
		entry.HasBatchSig = true
	}

	q.mu.Lock()
	q.entries = append(q.entries, entry)
	q.mu.Unlock()

	return nil
}

// DocCount returns the total number of documents across all block entries.
func (q *IndexerQueue) DocCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	total := 0
	for _, entry := range q.entries {
		total++ // block doc itself
		for _, packed := range entry.OtherDocIDs {
			total += len(packed) / uuidSize
		}
		if entry.HasBatchSig {
			total++
		}
	}
	return total
}

// entryDocCount returns the document count for a single entry (block + others + sig).
func entryDocCount(entry BlockEntry) int {
	count := 1 // block doc
	for _, packed := range entry.OtherDocIDs {
		count += len(packed) / uuidSize
	}
	if entry.HasBatchSig {
		count++
	}
	return count
}

// DrainByDocCount removes the oldest block entries until at least `excess` documents
// have been accumulated.
func (q *IndexerQueue) DrainByDocCount(excess int, blockCollectionName, blockSigCollectionName string) *DrainResult {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.entries) == 0 || excess <= 0 {
		return nil
	}

	// Sort by block number to ensure we drain the oldest blocks
	sort.Slice(q.entries, func(i, j int) bool {
		return q.entries[i].BlockNumber < q.entries[j].BlockNumber
	})

	// Walk from front, accumulate doc count until >= excess
	docsAccumulated := 0
	cutoff := 0
	for i, entry := range q.entries {
		docsAccumulated += entryDocCount(entry)
		if docsAccumulated >= excess {
			cutoff = i + 1
			break
		}
	}

	if cutoff == 0 {
		cutoff = len(q.entries)
	}

	drainCount := cutoff
	drained := make([]BlockEntry, drainCount)
	copy(drained, q.entries[:drainCount])

	remaining := make([]BlockEntry, len(q.entries)-drainCount)
	copy(remaining, q.entries[drainCount:])
	q.entries = remaining

	return q.buildDrainResult(drained, drainCount, blockCollectionName, blockSigCollectionName)
}

// Drain removes and returns the oldest entries, keeping only the last `keep` entries.
// Returns a DrainResult with docIDs grouped by collection name.
func (q *IndexerQueue) Drain(keep int, blockCollectionName, blockSigCollectionName string) *DrainResult {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.entries) <= keep {
		return nil
	}

	// Sort by block number to ensure we keep the highest-numbered blocks
	sort.Slice(q.entries, func(i, j int) bool {
		return q.entries[i].BlockNumber < q.entries[j].BlockNumber
	})

	drainCount := len(q.entries) - keep
	drained := make([]BlockEntry, drainCount)
	copy(drained, q.entries[:drainCount])

	remaining := make([]BlockEntry, keep)
	copy(remaining, q.entries[drainCount:])
	q.entries = remaining

	return q.buildDrainResult(drained, drainCount, blockCollectionName, blockSigCollectionName)
}

// buildDrainResult rebuilds DocIDsByCollection from the drained entries:
// block docIDs under the block collection name, batch-sig docIDs under the
// block-signature collection name, and all OtherDocIDs under their own keys.
func (q *IndexerQueue) buildDrainResult(drained []BlockEntry, drainCount int, blockCollectionName, blockSigCollectionName string) *DrainResult {
	result := &DrainResult{
		DocIDsByCollection: make(map[string][]string),
		BlockCount:         drainCount,
	}

	var blockIDs []string
	for _, entry := range drained {
		blockIDs = append(blockIDs, q.RestoreDocID(entry.BlockDocID))

		for colName, packed := range entry.OtherDocIDs {
			if ids := q.UnpackDocIDs(packed); len(ids) > 0 {
				result.DocIDsByCollection[colName] = append(
					result.DocIDsByCollection[colName], ids...)
			}
		}
		if entry.HasBatchSig {
			result.DocIDsByCollection[blockSigCollectionName] = append(
				result.DocIDsByCollection[blockSigCollectionName], q.RestoreDocID(entry.BatchSigID))
		}
	}

	if len(blockIDs) > 0 && blockCollectionName != "" {
		result.DocIDsByCollection[blockCollectionName] = blockIDs
	}

	return result
}

// Len returns the current number of entries in the queue.
func (q *IndexerQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries)
}

// HighestBlockNumber returns the highest block number in the queue, or 0 if empty.
func (q *IndexerQueue) HighestBlockNumber() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	var highest int64
	for _, entry := range q.entries {
		if entry.BlockNumber > highest {
			highest = entry.BlockNumber
		}
	}
	return highest
}

// ─── UUID packing helpers ─────────────────────────────────────────────────────

// extractUUID extracts the 16-byte UUID from a docID string and lazily
// initialises the queue's docIDPrefix from the first prefix seen.
func (q *IndexerQueue) extractUUID(docID string) ([uuidSize]byte, error) {
	_, after, ok := strings.Cut(docID, "-")
	if !ok {
		return [uuidSize]byte{}, fmt.Errorf("invalid docID format: %s", docID)
	}
	return parseUUIDHex(after)
}

// parseUUIDHex parses a UUID string (with hyphens) into 16 raw bytes.
func parseUUIDHex(uuidStr string) ([uuidSize]byte, error) {
	clean := strings.ReplaceAll(uuidStr, "-", "")
	if len(clean) != uuidHexWithoutHyphensLen {
		return [uuidSize]byte{}, fmt.Errorf("invalid UUID: %s", uuidStr)
	}
	var result [uuidSize]byte
	_, err := hex.Decode(result[:], []byte(clean))
	return result, err
}

// formatUUID formats 16 raw bytes as a UUID string with hyphens.
func formatUUID(b [uuidSize]byte) string {
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// RestoreDocID reconstructs a full docID string from packed UUID bytes.
func (q *IndexerQueue) RestoreDocID(uuid [uuidSize]byte) string {
	return docIDPrefix + "-" + formatUUID(uuid)
}

// packDocIDs converts docID strings to a single packed byte slice (16 bytes per UUID).
func (q *IndexerQueue) packDocIDs(docIDs []string) ([]byte, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}
	packed := make([]byte, 0, len(docIDs)*uuidSize)
	for _, id := range docIDs {
		uuid, err := q.extractUUID(id)
		if err != nil {
			return nil, err
		}
		packed = append(packed, uuid[:]...)
	}
	return packed, nil
}

// UnpackDocIDs converts packed UUID bytes back to docID strings.
func (q *IndexerQueue) UnpackDocIDs(packed []byte) []string {
	if len(packed) == 0 {
		return nil
	}
	count := len(packed) / uuidSize
	ids := make([]string, count)
	for i := range count {
		var uuid [uuidSize]byte
		copy(uuid[:], packed[i*uuidSize:(i+1)*uuidSize])
		ids[i] = q.RestoreDocID(uuid)
	}
	return ids
}
