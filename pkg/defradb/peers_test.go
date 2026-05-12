package defradb

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/require"
)

const (
	testPeer1Addr = "127.0.0.1:4001"
	testPeer1ID   = "12D3KooWBh1N2rLJc9Rj7Z3rX9Y8uMvN2pQ4sT7wX1yB6eF9hK3mP5sA8"
	testPeer1Full = testPeer1Addr + "/p2p/" + testPeer1ID
	testPeer2Addr = "192.168.1.100:4002"
	testPeer2ID   = "12D3KooWEj8q4q5r6s7t8u9v0w1x2y3z4a5b6c7d8e9f0g1h2i3j4k5l6m"
	testPeer2Full = testPeer2Addr + "/p2p/" + testPeer2ID
)

func TestBootstrapIntoPeers(t *testing.T) {
	tests := []struct {
		name           string
		input          []string
		expectedPeers  []client.PeerInfo
		expectedErrors int
	}{
		{
			name:  "valid single peer",
			input: []string{testPeer1Full},
			expectedPeers: []client.PeerInfo{
				{
					Addresses: []string{testPeer1Addr},
					ID:        testPeer1ID,
				},
			},
			expectedErrors: 0,
		},
		{
			name:  "valid multiple peers",
			input: []string{testPeer1Full, testPeer2Full},
			expectedPeers: []client.PeerInfo{
				{
					Addresses: []string{testPeer1Addr},
					ID:        testPeer1ID,
				},
				{
					Addresses: []string{testPeer2Addr},
					ID:        testPeer2ID,
				},
			},
			expectedErrors: 0,
		},
		{
			name:           "invalid peer format - missing /p2p/",
			input:          []string{"127.0.0.1:4001"},
			expectedPeers:  []client.PeerInfo{},
			expectedErrors: 1,
		},
		{
			name:           "invalid peer format - multiple /p2p/",
			input:          []string{"127.0.0.1:4001/p2p/12D3KooWBh1N2rLJc9Rj7Z3rX9Y8uMvN2pQ4sT7wX1yB6eF9hK3mP5sA8/p2p/extra"},
			expectedPeers:  []client.PeerInfo{},
			expectedErrors: 1,
		},
		{
			name:           "empty input",
			input:          []string{},
			expectedPeers:  []client.PeerInfo{},
			expectedErrors: 0,
		},
		{
			name:  "mixed valid and invalid peers",
			input: []string{testPeer1Full, "invalid", testPeer2Full},
			expectedPeers: []client.PeerInfo{
				{
					Addresses: []string{testPeer1Addr},
					ID:        testPeer1ID,
				},
				{
					Addresses: []string{testPeer2Addr},
					ID:        testPeer2ID,
				},
			},
			expectedErrors: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peers, errors := BootstrapIntoPeers(tt.input)

			if len(errors) != tt.expectedErrors {
				t.Errorf("Expected %d errors, got %d", tt.expectedErrors, len(errors))
			}

			if len(peers) != len(tt.expectedPeers) {
				t.Errorf("Expected %d peers, got %d", len(tt.expectedPeers), len(peers))
			}

			for i, expectedPeer := range tt.expectedPeers {
				if i >= len(peers) {
					t.Errorf("Expected peer at index %d but got none", i)
					continue
				}

				actualPeer := peers[i]
				if actualPeer.ID != expectedPeer.ID {
					t.Errorf("Expected peer ID %s, got %s", expectedPeer.ID, actualPeer.ID)
				}

				if len(actualPeer.Addresses) != len(expectedPeer.Addresses) {
					t.Errorf("Expected %d addresses, got %d", len(expectedPeer.Addresses), len(actualPeer.Addresses))
				}

				for j, expectedAddr := range expectedPeer.Addresses {
					if j >= len(actualPeer.Addresses) {
						t.Errorf("Expected address at index %d but got none", j)
						continue
					}
					if actualPeer.Addresses[j] != expectedAddr {
						t.Errorf("Expected address %s, got %s", expectedAddr, actualPeer.Addresses[j])
					}
				}
			}
		})
	}
}

func TestPeersIntoBootstrap(t *testing.T) {
	tests := []struct {
		name                 string
		input                []client.PeerInfo
		expectedBootstrap    []string
		expectedErrors       int
		expectedErrorIndices []int
	}{
		{
			name: "valid single peer",
			input: []client.PeerInfo{
				{
					Addresses: []string{testPeer1Addr},
					ID:        testPeer1ID,
				},
			},
			expectedBootstrap: []string{testPeer1Full},
			expectedErrors:    0,
		},
		{
			name: "valid multiple peers",
			input: []client.PeerInfo{
				{
					Addresses: []string{testPeer1Addr},
					ID:        testPeer1ID,
				},
				{
					Addresses: []string{testPeer2Addr},
					ID:        testPeer2ID,
				},
			},
			expectedBootstrap: []string{
				testPeer1Full,
				testPeer2Full,
			},
			expectedErrors: 0,
		},
		{
			name: "peer with empty ID",
			input: []client.PeerInfo{
				{
					Addresses: []string{testPeer1Addr},
					ID:        "",
				},
			},
			expectedBootstrap:    []string{},
			expectedErrors:       1,
			expectedErrorIndices: []int{0},
		},
		{
			name: "peer with no addresses",
			input: []client.PeerInfo{
				{
					Addresses: []string{},
					ID:        testPeer1ID,
				},
			},
			expectedBootstrap:    []string{},
			expectedErrors:       1,
			expectedErrorIndices: []int{0},
		},
		{
			name: "peer with multiple addresses - uses first",
			input: []client.PeerInfo{
				{
					Addresses: []string{testPeer1Addr, testPeer2Addr, "10.0.0.1:4003"},
					ID:        testPeer1ID,
				},
			},
			expectedBootstrap: []string{testPeer1Full},
			expectedErrors:    0,
		},
		{
			name:              "empty input",
			input:             []client.PeerInfo{},
			expectedBootstrap: []string{},
			expectedErrors:    0,
		},
		{
			name: "mixed valid and invalid peers",
			input: []client.PeerInfo{
				{
					Addresses: []string{testPeer1Addr},
					ID:        testPeer1ID,
				},
				{
					Addresses: []string{testPeer2Addr},
					ID:        "",
				},
				{
					Addresses: []string{},
					ID:        testPeer2ID,
				},
			},
			expectedBootstrap:    []string{testPeer1Full},
			expectedErrors:       2,
			expectedErrorIndices: []int{1, 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bootstrapPeers, errors := PeersIntoBootstrap(tt.input)

			if len(errors) != tt.expectedErrors {
				t.Errorf("Expected %d errors, got %d", tt.expectedErrors, len(errors))
			}

			if len(bootstrapPeers) != len(tt.expectedBootstrap) {
				t.Errorf("Expected %d bootstrap peers, got %d", len(tt.expectedBootstrap), len(bootstrapPeers))
			}

			for i, expectedBootstrap := range tt.expectedBootstrap {
				if i >= len(bootstrapPeers) {
					t.Errorf("Expected bootstrap peer at index %d but got none", i)
					continue
				}
				if bootstrapPeers[i] != expectedBootstrap {
					t.Errorf("Expected bootstrap peer %s, got %s", expectedBootstrap, bootstrapPeers[i])
				}
			}

			// Verify error indices if specified
			if tt.expectedErrorIndices != nil {
				for i, expectedIdx := range tt.expectedErrorIndices {
					if i >= len(errors) {
						t.Errorf("Expected error at index %d but got none", i)
						continue
					}
					// Check that the error message contains the expected index
					errorMsg := errors[i].Error()
					expectedIdxStr := fmt.Sprintf("index %d", expectedIdx)
					if !strings.Contains(errorMsg, expectedIdxStr) {
						t.Errorf("Expected error message to contain '%s', got: %s", expectedIdxStr, errorMsg)
					}
				}
			}
		})
	}
}

func TestBootstrapIntoPeersAndBack(t *testing.T) {
	// Test round-trip conversion
	originalBootstrap := []string{
		testPeer1Full,
		testPeer2Full,
	}

	// Convert bootstrap strings to peers
	peers, errors := BootstrapIntoPeers(originalBootstrap)
	if len(errors) > 0 {
		t.Errorf("Unexpected errors during bootstrap to peers conversion: %v", errors)
	}

	// Convert peers back to bootstrap strings
	convertedBootstrap, errors := PeersIntoBootstrap(peers)
	if len(errors) > 0 {
		t.Errorf("Unexpected errors during peers to bootstrap conversion: %v", errors)
	}

	// Verify round-trip conversion
	if len(convertedBootstrap) != len(originalBootstrap) {
		t.Errorf("Expected %d bootstrap peers after round-trip, got %d", len(originalBootstrap), len(convertedBootstrap))
	}

	for i, original := range originalBootstrap {
		if i >= len(convertedBootstrap) {
			t.Errorf("Expected bootstrap peer at index %d but got none", i)
			continue
		}
		if convertedBootstrap[i] != original {
			t.Errorf("Expected bootstrap peer %s, got %s", original, convertedBootstrap[i])
		}
	}
}

func TestConnectToPeers(t *testing.T) {
	t.Run("nil node should panic", func(t *testing.T) {
		ctx := context.Background()
		peers := []client.PeerInfo{
			{
				Addresses: []string{testPeer1Addr},
				ID:        testPeer1ID,
			},
		}

		// This should panic with nil node
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Expected function to panic with nil node, but it didn't")
			}
		}()

		peerString, errors := PeersIntoBootstrap(peers)
		require.Len(t, errors, 0)

		connectToPeers(ctx, nil, peerString)
	})

	t.Run("empty peers list", func(t *testing.T) {
		ctx := context.Background()
		peers := []string{}

		// This should not panic even with nil node since there are no peers to connect to
		err := connectToPeers(ctx, nil, peers)

		require.NoError(t, err)
	})

	t.Run("connect to valid peers", func(t *testing.T) {
		ctx := context.Background()

		// Start a test Defra node
		testConfig := DefaultConfig
		testNode, err := StartDefraInstanceWithTestConfig(t, testConfig, &MockSchemaApplierThatSucceeds{})
		if err != nil {
			t.Fatalf("Failed to start test Defra node: %v", err)
		}
		defer testNode.Close(ctx)

		// Create some valid peer info (these will fail to connect since they're not real peers, but should not panic)
		peers := []client.PeerInfo{
			{
				Addresses: []string{testPeer1Addr},
				ID:        testPeer1ID,
			},
			{
				Addresses: []string{testPeer2Addr},
				ID:        testPeer2ID,
			},
		}
		peerStrings, errors := PeersIntoBootstrap(peers)
		if len(errors) > 0 {
			t.Errorf("Errors translating peers into bootstrap format: %v", errors)
		}

		// This should not panic and should return connection errors (since these are fake peers)
		err = connectToPeers(ctx, testNode, peerStrings)
		require.Error(t, err)
	})

	t.Run("connect to empty peers list with real node", func(t *testing.T) {
		ctx := context.Background()

		// Start a test Defra node
		testConfig := DefaultConfig
		testNode, err := StartDefraInstanceWithTestConfig(t, testConfig, &MockSchemaApplierThatSucceeds{})
		if err != nil {
			t.Fatalf("Failed to start test Defra node: %v", err)
		}
		defer testNode.Close(ctx)

		peers := []string{}

		// This should not panic and should return no errors
		err = connectToPeers(ctx, testNode, peers)
		require.NoError(t, err)
	})

	t.Run("connect multiple nodes to each other", func(t *testing.T) {
		ctx := context.Background()

		// Start first Defra node with a specific listen address
		testConfig1 := DefaultConfig
		testConfig1.DefraDB.P2P.ListenAddr = "/ip4/127.0.0.1/tcp/9171"
		node1, err := StartDefraInstanceWithTestConfig(t, testConfig1, &MockSchemaApplierThatSucceeds{})
		if err != nil {
			t.Fatalf("Failed to start first Defra node: %v", err)
		}
		defer node1.Close(ctx)

		// Start second Defra node with a different listen address
		testConfig2 := DefaultConfig
		testConfig2.DefraDB.P2P.ListenAddr = "/ip4/127.0.0.1/tcp/9172"
		node2, err := StartDefraInstanceWithTestConfig(t, testConfig2, &MockSchemaApplierThatSucceeds{})
		if err != nil {
			t.Fatalf("Failed to start second Defra node: %v", err)
		}
		defer node2.Close(ctx)

		// Get the peer info from node1 to connect node2 to it
		node1PeerInfo, err := node1.DB.PeerInfo(ctx)
		require.NoError(t, err)

		// Now connect node2 to node1 using our connectToPeers function
		err = connectToPeers(ctx, node2, node1PeerInfo)
		require.NoError(t, err)

		// Test connecting node1 to node2 as well (bidirectional connection)
		node2PeerInfo, err := node2.DB.PeerInfo(ctx)
		require.NoError(t, err)

		err = connectToPeers(ctx, node1, node2PeerInfo)
		require.NoError(t, err)
	})
}
