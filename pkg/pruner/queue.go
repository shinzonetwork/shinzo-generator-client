package pruner

// PrunerQueue is the interface for queue implementations used by the pruner.
// Implemented by IndexerQueue - a compact packed-UUID queue for indexers that know docIDs at creation time
// Potentially suitable for common library.
type PrunerQueue interface {
	// Len returns the total number of entries in the queue.
	Len() int

	// Save persists the queue to disk. No-op if no file path was set.
	Save() error
}

// DrainResult holds docIDs grouped by collection name, ready for deletion.
type DrainResult struct {
	// DocIDsByCollection maps collection name → list of docIDs to delete.
	DocIDsByCollection map[string][]string
	// BlockCount is the number of blocks being drained.
	BlockCount int
}
