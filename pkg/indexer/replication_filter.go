package indexer

import "context"

// indexerReplicationFilter rejects all incoming P2P documents.
// The indexer is the source of truth (indexes directly from chain),
// so it should never accept data from peers. Without this filter,
// a host connected to two indexers would relay data between them,
// causing duplicate CRDT DAG entries and bloated storage.
type indexerReplicationFilter struct{}

// AllowReplication implements client.ReplicationFilter.
// Always returns false — the indexer does not accept incoming P2P documents.
func (f *indexerReplicationFilter) AllowReplication(_ context.Context, _ string, _ string, _ map[string]any) bool {
	return false
}
