package indexer

import "context"

// indexerReplicationFilter rejects all incoming P2P documents.
// The indexer is the source of truth (indexes directly from chain),
// so it should not accept data from peers. Without this filter,
// a host connected to two indexers would relay block/snapshot signatures
// between them, creating duplicate documents with different signer CIDs.
type indexerReplicationFilter struct{}

// AllowReplication implements client.ReplicationFilter.
// Always returns false — the indexer does not accept incoming P2P documents.
func (f *indexerReplicationFilter) AllowReplication(_ context.Context, _ string, _ string, _ map[string]any) bool {
	return false
}
