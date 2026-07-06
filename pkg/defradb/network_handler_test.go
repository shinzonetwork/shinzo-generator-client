package defradb

import (
	"context"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
)

func TestNetworkHandlerStateManagement(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			P2P: config.DefraDBP2PConfig{
				Enabled:             true,
				BootstrapPeers:      []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWTestPeer1"},
				MaxRetries:          2,
				RetryBaseDelayMs:    100,
				ReconnectIntervalMs: 1000,
				EnableAutoReconnect: true,
			},
		},
	}

	// Test NewNetworkHandler initialization
	t.Run("NewNetworkHandler initializes correctly", func(t *testing.T) {
		ctx := context.Background()
		nh := NewNetworkHandler(&ctx, nil, cfg)

		if nh.hostRunning != true {
			t.Error("hostRunning should be true on init")
		}
		if nh.networkActive != false {
			t.Error("networkActive should be false on init")
		}
		if len(nh.peers) != 1 {
			t.Errorf("expected 1 peer, got %d", len(nh.peers))
		}
	})

	// Test IsHostRunning / IsNetworkActive decoupling
	t.Run("Host and Network states are decoupled", func(t *testing.T) {
		ctx := context.Background()
		nh := NewNetworkHandler(&ctx, nil, cfg)

		if !nh.IsHostRunning() {
			t.Error("host should be running")
		}
		if nh.IsNetworkActive() {
			t.Error("network should not be active yet")
		}

		nh.SetHostRunning(false)
		if nh.IsHostRunning() {
			t.Error("host should not be running after SetHostRunning(false)")
		}
	})

	// Test AddPeer / RemovePeer
	t.Run("AddPeer and RemovePeer work correctly", func(t *testing.T) {
		ctx := context.Background()
		nh := NewNetworkHandler(&ctx, nil, cfg)

		newPeer := "/ip4/192.168.1.1/tcp/4001/p2p/12D3KooWTestPeer2"

		err := nh.AddPeer(&ctx, newPeer)
		if err != nil {
			t.Errorf("AddPeer failed: %v", err)
		}

		peers := nh.GetPeers()
		if len(peers) != 2 {
			t.Errorf("expected 2 peers after add, got %d", len(peers))
		}

		err = nh.AddPeer(&ctx, newPeer)
		if err == nil {
			t.Error("AddPeer should fail for duplicate peer")
		}

		err = nh.RemovePeer(newPeer)
		if err != nil {
			t.Errorf("RemovePeer failed: %v", err)
		}

		peers = nh.GetPeers()
		if len(peers) != 1 {
			t.Errorf("expected 1 peer after remove, got %d", len(peers))
		}

		err = nh.RemovePeer(newPeer)
		if err == nil {
			t.Error("RemovePeer should fail for non-existent peer")
		}
	})

	// Test GetConnectionStats
	t.Run("GetConnectionStats returns correct counts", func(t *testing.T) {
		ctx := context.Background()
		nh := NewNetworkHandler(&ctx, nil, cfg)

		stats := nh.GetConnectionStats()

		if stats.TotalPeers != 1 {
			t.Errorf("expected 1 total peer, got %d", stats.TotalPeers)
		}
		if stats.DisconnectedPeers != 1 {
			t.Errorf("expected 1 disconnected peer, got %d", stats.DisconnectedPeers)
		}
		if stats.HostRunning != true {
			t.Error("HostRunning should be true")
		}
		if stats.NetworkActive != false {
			t.Error("NetworkActive should be false")
		}
	})

	// Test PeerState
	t.Run("GetPeerState returns correct state", func(t *testing.T) {
		ctx := context.Background()
		nh := NewNetworkHandler(&ctx, nil, cfg)

		peerAddr := cfg.DefraDB.P2P.BootstrapPeers[0]
		state, exists := nh.GetPeerState(peerAddr)

		if !exists {
			t.Error("peer should exist")
		}
		if state.State != StateDisconnected {
			t.Errorf("expected StateDisconnected, got %v", state.State)
		}
		if state.Address != peerAddr {
			t.Errorf("expected address %s, got %s", peerAddr, state.Address)
		}

		_, exists = nh.GetPeerState("/ip4/1.2.3.4/tcp/4001/p2p/nonexistent")
		if exists {
			t.Error("non-existent peer should not exist")
		}
	})
}

func TestConnectionStateString(t *testing.T) {
	tests := []struct {
		state    ConnectionState
		expected string
	}{
		{StateDisconnected, "DISCONNECTED"},
		{StateConnecting, "CONNECTING"},
		{StateConnected, "CONNECTED"},
		{StateReconnecting, "RECONNECTING"},
		{StateFailed, "FAILED"},
		{ConnectionState(99), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("ConnectionState.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestPeerStateMethods(t *testing.T) {
	t.Run("IsConnected", func(t *testing.T) {
		ps := &PeerState{State: StateConnected}
		if !ps.IsConnected() {
			t.Error("should be connected")
		}

		ps.State = StateDisconnected
		if ps.IsConnected() {
			t.Error("should not be connected")
		}
	})

	t.Run("IsReachable", func(t *testing.T) {
		ps := &PeerState{State: StateDisconnected}
		if !ps.IsReachable() {
			t.Error("disconnected peer should be reachable")
		}

		ps.State = StateFailed
		if ps.IsReachable() {
			t.Error("failed peer should not be reachable")
		}
	})

	t.Run("Copy creates independent copy", func(t *testing.T) {
		original := &PeerState{
			Address:     "test-addr",
			State:       StateConnected,
			LastAttempt: time.Now(),
			RetryCount:  3,
		}

		copied := original.Copy()

		original.State = StateDisconnected
		original.RetryCount = 5

		if copied.State != StateConnected {
			t.Error("copy state should not change")
		}
		if copied.RetryCount != 3 {
			t.Error("copy retry count should not change")
		}
	})
}

func TestExtractPeerID(t *testing.T) {
	tests := []struct {
		name      string
		multiaddr string
		expected  string
	}{
		{
			name:      "standard multiaddr with p2p",
			multiaddr: "/ip4/34.10.129.37/tcp/9171/p2p/12D3KooWJXpGMpV2wGS6q3pU1j9E8J4nbprzMoudeQTRLoXmMy8x",
			expected:  "12D3KooWJXpGMpV2wGS6q3pU1j9E8J4nbprzMoudeQTRLoXmMy8x",
		},
		{
			name:      "localhost multiaddr",
			multiaddr: "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWTestPeer1",
			expected:  "12D3KooWTestPeer1",
		},
		{
			name:      "multiaddr without p2p",
			multiaddr: "/ip4/192.168.1.1/tcp/9171",
			expected:  "",
		},
		{
			name:      "empty string",
			multiaddr: "",
			expected:  "",
		},
		{
			name:      "just peer ID",
			multiaddr: "/p2p/12D3KooWSimplePeerID",
			expected:  "12D3KooWSimplePeerID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPeerID(tt.multiaddr)
			if got != tt.expected {
				t.Errorf("extractPeerID(%q) = %q, want %q", tt.multiaddr, got, tt.expected)
			}
		})
	}
}
