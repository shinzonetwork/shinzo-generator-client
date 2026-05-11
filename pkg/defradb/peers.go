package defradb

import (
	"context"
	"fmt"
	"strings"

	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/node"
)

const bootstrapPeerMultiaddrParts = 2 // [address, peerID] split on "/p2p/"

// BootstrapIntoPeers converts bootstrap peer strings into DefraDB peer info values.
func BootstrapIntoPeers(configuredBootstrapPeers []string) ([]client.PeerInfo, []error) {
	peers := []client.PeerInfo{}
	errors := []error{}

	for i, peer := range configuredBootstrapPeers {
		parts := strings.Split(peer, "/p2p/")
		if len(parts) != bootstrapPeerMultiaddrParts {
			errors = append(errors, fmt.Errorf("peer at index %d is invalid and will be skipped. Given: %v", i, configuredBootstrapPeers))
			continue
		}
		address := parts[0]
		peerID := parts[1]

		peerInfo := client.PeerInfo{
			Addresses: []string{address},
			ID:        peerID,
		}
		peers = append(peers, peerInfo)
	}

	return peers, errors
}

// PeersIntoBootstrap converts DefraDB peer info values into bootstrap peer strings.
func PeersIntoBootstrap(peers []client.PeerInfo) ([]string, []error) {
	bootstrapPeers := []string{}
	errors := []error{}

	for i, peer := range peers {
		if peer.ID == "" {
			errors = append(errors, fmt.Errorf("peer at index %d has empty ID and will be skipped", i))
			continue
		}

		if len(peer.Addresses) == 0 {
			errors = append(errors, fmt.Errorf("peer at index %d has no addresses and will be skipped", i))
			continue
		}

		// Use the first address if multiple addresses are provided
		address := peer.Addresses[0]
		bootstrapPeer := fmt.Sprintf("%s/p2p/%s", address, peer.ID)
		bootstrapPeers = append(bootstrapPeers, bootstrapPeer)
	}

	return bootstrapPeers, errors
}

func connectToPeers(ctx context.Context, defraNode *node.Node, peers []string) error {
	if len(peers) == 0 {
		return nil
	}

	err := defraNode.DB.Connect(ctx, peers)
	if err != nil {
		return fmt.Errorf("error connecting to peer: %w", err)
	}

	return nil
}
