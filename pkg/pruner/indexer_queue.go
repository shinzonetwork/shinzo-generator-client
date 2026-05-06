package pruner

import (
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

const uuidSize = 16

// docIDPrefix is the constant version prefix shared by all DefraDB document IDs.
var (
	docIDPrefix     string
	docIDPrefixOnce sync.Once
)

// BlockEntry holds all document IDs created for a single block.
type BlockEntry struct {
	BlockNumber    int64
	BlockDocID     [uuidSize]byte // single UUID for the block document
	TransactionIDs []byte         // packed UUIDs: len/16 = count
	LogIDs         []byte         // packed UUIDs: len/16 = count
	AccessListIDs  []byte         // packed UUIDs: len/16 = count
	BatchSigID     [uuidSize]byte // single UUID for batch signature
	HasBatchSig    bool
}

// indexerQueueSnapshot is the serializable form of the queue.
type indexerQueueSnapshot struct {
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
		entries: make([]BlockEntry, 0, 128),
	}
}

// LoadFromFile loads queue entries from a gob-encoded file.
// Returns the number of entries loaded. If the file doesn't exist, returns 0.
func (q *IndexerQueue) LoadFromFile(path string) (int, error) {
	q.filePath = path

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to open queue file: %w", err)
	}
	defer f.Close()

	var snap indexerQueueSnapshot
	if err := gob.NewDecoder(f).Decode(&snap); err != nil {
		return 0, fmt.Errorf("failed to decode queue file: %w", err)
	}

	q.mu.Lock()
	q.entries = snap.Entries
	count := len(q.entries)
	q.mu.Unlock()

	if snap.DocIDPrefix != "" {
		docIDPrefixOnce.Do(func() {
			docIDPrefix = snap.DocIDPrefix
		})
	}

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
		DocIDPrefix: docIDPrefix,
		Entries:     make([]BlockEntry, len(q.entries)),
	}
	copy(snap.Entries, q.entries)
	q.mu.Unlock()

	if len(snap.Entries) == 0 {
		os.Remove(q.filePath)
		return nil
	}

	tmpPath := q.filePath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	if err := gob.NewEncoder(f).Encode(snap); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to encode queue: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, q.filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// TrackBlockDocIDs adds a block's docIDs to the queue.
// blockDocID is the block document ID, otherDocIDs maps collection name → docID list.
func (q *IndexerQueue) TrackBlockDocIDs(blockNumber int64, blockDocID string, otherDocIDs map[string][]string, batchSigID string) error {
	entry := BlockEntry{
		BlockNumber: blockNumber,
	}

	if blockDocID != "" {
		uuid, err := extractUUID(blockDocID)
		if err != nil {
			return fmt.Errorf("invalid block docID: %w", err)
		}
		entry.BlockDocID = uuid
	}

	// Pack transaction IDs
	if txIDs, ok := otherDocIDs[constants.CollectionTransaction]; ok && len(txIDs) > 0 {
		packed, err := packDocIDs(txIDs)
		if err != nil {
			return fmt.Errorf("invalid tx docID: %w", err)
		}
		entry.TransactionIDs = packed
	}

	// Pack log IDs
	if logIDs, ok := otherDocIDs[constants.CollectionLog]; ok && len(logIDs) > 0 {
		packed, err := packDocIDs(logIDs)
		if err != nil {
			return fmt.Errorf("invalid log docID: %w", err)
		}
		entry.LogIDs = packed
	}

	// Pack access list entry IDs
	if aleIDs, ok := otherDocIDs[constants.CollectionAccessListEntry]; ok && len(aleIDs) > 0 {
		packed, err := packDocIDs(aleIDs)
		if err != nil {
			return fmt.Errorf("invalid ALE docID: %w", err)
		}
		entry.AccessListIDs = packed
	}

	if batchSigID != "" {
		uuid, err := extractUUID(batchSigID)
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
		total += 1 // block doc itself
		total += len(entry.TransactionIDs) / uuidSize
		total += len(entry.LogIDs) / uuidSize
		total += len(entry.AccessListIDs) / uuidSize
		if entry.HasBatchSig {
			total++
		}
	}
	return total
}

// DrainByDocCount removes the oldest block entries until at least `excess` documents
// have been accumulated.
func (q *IndexerQueue) DrainByDocCount(excess int, collections CollectionConfig) *DrainResult {
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
		docsAccumulated += 1 // block doc
		docsAccumulated += len(entry.TransactionIDs) / uuidSize
		docsAccumulated += len(entry.LogIDs) / uuidSize
		docsAccumulated += len(entry.AccessListIDs) / uuidSize
		if entry.HasBatchSig {
			docsAccumulated++
		}
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

	// Build DrainResult grouped by collection
	result := &DrainResult{
		DocIDsByCollection: make(map[string][]string),
		BlockCount:         drainCount,
	}

	var blockIDs []string
	for _, entry := range drained {
		blockIDs = append(blockIDs, RestoreDocID(entry.BlockDocID))

		if txIDs := UnpackDocIDs(entry.TransactionIDs); len(txIDs) > 0 {
			result.DocIDsByCollection[constants.CollectionTransaction] = append(
				result.DocIDsByCollection[constants.CollectionTransaction], txIDs...)
		}
		if logIDs := UnpackDocIDs(entry.LogIDs); len(logIDs) > 0 {
			result.DocIDsByCollection[constants.CollectionLog] = append(
				result.DocIDsByCollection[constants.CollectionLog], logIDs...)
		}
		if aleIDs := UnpackDocIDs(entry.AccessListIDs); len(aleIDs) > 0 {
			result.DocIDsByCollection[constants.CollectionAccessListEntry] = append(
				result.DocIDsByCollection[constants.CollectionAccessListEntry], aleIDs...)
		}
		if entry.HasBatchSig {
			result.DocIDsByCollection[constants.CollectionBatchSignature] = append(
				result.DocIDsByCollection[constants.CollectionBatchSignature], RestoreDocID(entry.BatchSigID))
		}
	}

	if len(blockIDs) > 0 {
		result.DocIDsByCollection[collections.BlockCollection] = blockIDs
	}

	return result
}

// Drain removes and returns the oldest entries, keeping only the last `keep` entries.
// Returns a DrainResult with docIDs grouped by collection name.
func (q *IndexerQueue) Drain(keep int, collections CollectionConfig) *DrainResult {
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

	// Build DrainResult grouped by collection
	result := &DrainResult{
		DocIDsByCollection: make(map[string][]string),
		BlockCount:         drainCount,
	}

	var blockIDs []string
	for _, entry := range drained {
		blockIDs = append(blockIDs, RestoreDocID(entry.BlockDocID))

		if txIDs := UnpackDocIDs(entry.TransactionIDs); len(txIDs) > 0 {
			result.DocIDsByCollection[constants.CollectionTransaction] = append(
				result.DocIDsByCollection[constants.CollectionTransaction], txIDs...)
		}
		if logIDs := UnpackDocIDs(entry.LogIDs); len(logIDs) > 0 {
			result.DocIDsByCollection[constants.CollectionLog] = append(
				result.DocIDsByCollection[constants.CollectionLog], logIDs...)
		}
		if aleIDs := UnpackDocIDs(entry.AccessListIDs); len(aleIDs) > 0 {
			result.DocIDsByCollection[constants.CollectionAccessListEntry] = append(
				result.DocIDsByCollection[constants.CollectionAccessListEntry], aleIDs...)
		}
		if entry.HasBatchSig {
			result.DocIDsByCollection[constants.CollectionBatchSignature] = append(
				result.DocIDsByCollection[constants.CollectionBatchSignature], RestoreDocID(entry.BatchSigID))
		}
	}

	if len(blockIDs) > 0 {
		result.DocIDsByCollection[collections.BlockCollection] = blockIDs
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

// ─── UUID packing helpers (exported for use by consumers) ────────────────────

// ExtractUUID extracts the 16-byte UUID from a docID string.
func extractUUID(docID string) ([uuidSize]byte, error) {
	idx := strings.IndexByte(docID, '-')
	if idx < 0 {
		return [uuidSize]byte{}, fmt.Errorf("invalid docID format: %s", docID)
	}
	docIDPrefixOnce.Do(func() {
		docIDPrefix = docID[:idx]
	})
	return parseUUIDHex(docID[idx+1:])
}

// parseUUIDHex parses a UUID string (with hyphens) into 16 raw bytes.
func parseUUIDHex(uuidStr string) ([uuidSize]byte, error) {
	clean := strings.ReplaceAll(uuidStr, "-", "")
	if len(clean) != 32 {
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
func RestoreDocID(uuid [uuidSize]byte) string {
	return docIDPrefix + "-" + formatUUID(uuid)
}

// packDocIDs converts docID strings to a single packed byte slice (16 bytes per UUID).
func packDocIDs(docIDs []string) ([]byte, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}
	packed := make([]byte, 0, len(docIDs)*uuidSize)
	for _, id := range docIDs {
		uuid, err := extractUUID(id)
		if err != nil {
			return nil, err
		}
		packed = append(packed, uuid[:]...)
	}
	return packed, nil
}

// UnpackDocIDs converts packed UUID bytes back to docID strings.
func UnpackDocIDs(packed []byte) []string {
	if len(packed) == 0 {
		return nil
	}
	count := len(packed) / uuidSize
	ids := make([]string, count)
	for i := 0; i < count; i++ {
		var uuid [uuidSize]byte
		copy(uuid[:], packed[i*uuidSize:(i+1)*uuidSize])
		ids[i] = RestoreDocID(uuid)
	}
	return ids
}