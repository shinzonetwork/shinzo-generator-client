package defradb

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/sourcenetwork/defradb/event"
	"github.com/sourcenetwork/defradb/node"
)

const maxPeerConnectBackoff = 30 * time.Second

// NetworkHandler manages P2P networking for DefraDB.
type NetworkHandler struct {
	node *node.Node
	cfg  *config.Config

	// State management
	hostRunning   bool
	networkActive bool

	// Peer management
	peers          map[string]*PeerState
	peersMu        sync.RWMutex
	bootstrapPeers []string

	// Reconnection management
	reconnectTicker *time.Ticker
	reconnectStop   chan struct{}

	// Context management
	ctx    context.Context //nolint:containedctx // used for lifecycle management of background goroutines
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewNetworkHandler creates a new network handler.
func NewNetworkHandler(defraNode *node.Node, cfg *config.Config) *NetworkHandler {
	ctx, cancel := context.WithCancel(context.Background())

	peers := make(map[string]*PeerState)
	for _, addr := range cfg.DefraDB.P2P.BootstrapPeers {
		peers[addr] = &PeerState{
			Address: addr,
			State:   StateDisconnected,
		}
	}

	if cfg.DefraDB.P2P.MaxRetries == 0 {
		cfg.DefraDB.P2P.MaxRetries = 5
	}
	if cfg.DefraDB.P2P.RetryBaseDelayMs == 0 {
		cfg.DefraDB.P2P.RetryBaseDelayMs = 1000
	}
	if cfg.DefraDB.P2P.ReconnectIntervalMs == 0 {
		cfg.DefraDB.P2P.ReconnectIntervalMs = 60000
	}

	return &NetworkHandler{
		node:           defraNode,
		cfg:            cfg,
		hostRunning:    true,
		networkActive:  false,
		peers:          peers,
		bootstrapPeers: cfg.DefraDB.P2P.BootstrapPeers,
		ctx:            ctx,
		cancel:         cancel,
	}
}

// StartNetwork activates P2P networking and begins connection attempts.
func (nh *NetworkHandler) StartNetwork() error {
	nh.peersMu.Lock()
	defer nh.peersMu.Unlock()

	if nh.networkActive {
		logger.Sugar.Info("P2P network already active")
		return nil
	}

	logger.Sugar.Info("Starting P2P network connections...")

	var connectedCount int
	for addr := range nh.peers {
		if err := nh.connectWithRetryLocked(addr); err != nil {
			logger.Sugar.Warnf("Failed to connect to peer %s: %v", addr, err)
		} else {
			connectedCount++
		}
	}

	nh.networkActive = true

	nh.startReconnectionLoop()

	logger.Sugar.Infof("P2P network activated, connected to %d/%d peers", connectedCount, len(nh.peers))
	return nil
}

// StopNetwork deactivates P2P networking gracefully.
func (nh *NetworkHandler) StopNetwork() error {
	nh.peersMu.Lock()

	if !nh.networkActive {
		nh.peersMu.Unlock()
		logger.Sugar.Info("P2P network already inactive")
		return nil
	}

	logger.Sugar.Info("Stopping P2P network connections...")

	if nh.reconnectStop != nil {
		close(nh.reconnectStop)
		nh.reconnectStop = nil
	}

	nh.cancel()

	nh.networkActive = false

	for _, peer := range nh.peers {
		peer.State = StateDisconnected
		peer.ConnectedAt = time.Time{}
	}

	nh.peersMu.Unlock()

	nh.wg.Wait()

	// Create new context for future use
	nh.ctx, nh.cancel = context.WithCancel(context.Background())

	logger.Sugar.Info("P2P network deactivated")
	return nil
}

// IsNetworkActive returns whether P2P networking is currently active.
func (nh *NetworkHandler) IsNetworkActive() bool {
	nh.peersMu.RLock()
	defer nh.peersMu.RUnlock()
	return nh.networkActive
}

// IsHostRunning returns whether the host is running.
func (nh *NetworkHandler) IsHostRunning() bool {
	nh.peersMu.RLock()
	defer nh.peersMu.RUnlock()
	return nh.hostRunning
}

// SetHostRunning updates the host running state.
func (nh *NetworkHandler) SetHostRunning(running bool) {
	nh.peersMu.Lock()
	defer nh.peersMu.Unlock()
	nh.hostRunning = running
}

// ToggleNetwork switches P2P networking on/off.
func (nh *NetworkHandler) ToggleNetwork() error {
	if nh.IsNetworkActive() {
		return nh.StopNetwork()
	}
	return nh.StartNetwork()
}

// connectWithRetryLocked attempts to connect to a peer with exponential backoff.
func (nh *NetworkHandler) connectWithRetryLocked(peerAddr string) error {
	peer, exists := nh.peers[peerAddr]
	if !exists {
		return fmt.Errorf("peer not found: %s", peerAddr)
	}

	maxRetries := nh.cfg.DefraDB.P2P.MaxRetries
	baseDelay := time.Duration(nh.cfg.DefraDB.P2P.RetryBaseDelayMs) * time.Millisecond

	peer.State = StateConnecting
	peer.RetryCount = 0

	for attempt := range maxRetries {
		peer.LastAttempt = time.Now()
		peer.RetryCount = attempt + 1

		nh.peersMu.Unlock()
		err := connectToPeers(nh.ctx, nh.node, []string{peerAddr})
		nh.peersMu.Lock()

		if err == nil {
			peer.State = StateConnected
			peer.ConnectedAt = time.Now()
			peer.LastError = nil
			logger.Sugar.Infof("Connected to peer %s on attempt %d", peerAddr, attempt+1)
			return nil
		}

		peer.LastError = err

		select {
		case <-nh.ctx.Done():
			peer.State = StateDisconnected
			return nh.ctx.Err()
		default:
		}

		delay := min(baseDelay*time.Duration(1<<attempt), maxPeerConnectBackoff)

		logger.Sugar.Debugf("Connection attempt %d/%d to %s failed: %v. Retrying in %v",
			attempt+1, maxRetries, peerAddr, err, delay)

		nh.peersMu.Unlock()
		select {
		case <-nh.ctx.Done():
			nh.peersMu.Lock()
			peer.State = StateDisconnected
			return nh.ctx.Err()
		case <-time.After(delay):
		}
		nh.peersMu.Lock()
	}

	peer.State = StateFailed
	return fmt.Errorf("failed to connect to peer %s after %d retries: %w", peerAddr, maxRetries, peer.LastError)
}

// startReconnectionLoop starts the background reconnection goroutine.
func (nh *NetworkHandler) startReconnectionLoop() {
	if !nh.cfg.DefraDB.P2P.EnableAutoReconnect {
		return
	}
	interval := time.Duration(nh.cfg.DefraDB.P2P.ReconnectIntervalMs) * time.Millisecond
	nh.reconnectStop = make(chan struct{})
	nh.reconnectTicker = time.NewTicker(interval)
	nh.wg.Go(func() {
		defer nh.reconnectTicker.Stop()
		for {
			select {
			case <-nh.reconnectStop:
				return
			case <-nh.ctx.Done():
				return
			case <-nh.reconnectTicker.C:
				nh.checkPeerHealth()
				nh.reconnectDisconnectedPeers()
			}
		}
	})
	nh.startNoPeersEventListener()
}

// startNoPeersEventListener subscribes to P2PNoPeers events and triggers immediate reconnection.
func (nh *NetworkHandler) startNoPeersEventListener() {
	if nh.node == nil || nh.node.DB == nil {
		return
	}
	sub, err := nh.node.DB.Events().Subscribe(event.P2PNoPeersName)
	if err != nil {
		logger.Sugar.Warnf("Failed to subscribe to P2PNoPeers events: %v", err)
		return
	}
	nh.wg.Go(func() {
		for {
			select {
			case <-nh.reconnectStop:
				return
			case <-nh.ctx.Done():
				return
			case msg, ok := <-sub.Message():
				if !ok {
					return
				}
				if _, ok := msg.Data.(event.P2PNoPeers); ok {
					nh.forceReconnectAll()
				}
			}
		}
	})
	logger.Sugar.Info("P2PNoPeers event listener started")
}

// forceReconnectAll marks all peers as disconnected and triggers immediate reconnection.
func (nh *NetworkHandler) forceReconnectAll() {
	nh.peersMu.Lock()
	for _, peer := range nh.peers {
		if peer.State == StateConnected {
			peer.State = StateDisconnected
			peer.ConnectedAt = time.Time{}
			peer.LastError = fmt.Errorf("P2P mesh lost - no active peers")
		}
	}
	nh.peersMu.Unlock()
	nh.reconnectDisconnectedPeers()
}

// checkPeerHealth verifies that peers we think are connected are still connected.
func (nh *NetworkHandler) checkPeerHealth() {
	if nh.node == nil || nh.node.DB == nil {
		return
	}

	peers, err := nh.node.DB.ActivePeers(nh.ctx)
	if err != nil {
		logger.Sugar.Debugf("Failed to get peer info from DefraDB: %v", err)
		return
	}

	connectedPeers := make(map[string]bool)
	for _, peerAddr := range peers {
		connectedPeers[peerAddr] = true
		if peerID := extractPeerID(peerAddr); peerID != "" {
			connectedPeers[peerID] = true
		}
	}

	nh.peersMu.Lock()
	defer nh.peersMu.Unlock()

	for addr, peer := range nh.peers {
		if peer.State != StateConnected {
			continue
		}

		stillConnected := false
		if connectedPeers[addr] {
			stillConnected = true
		} else {
			trackedPeerID := extractPeerID(addr)
			if trackedPeerID != "" && connectedPeers[trackedPeerID] {
				stillConnected = true
			}
		}

		if !stillConnected {
			peer.State = StateDisconnected
			peer.ConnectedAt = time.Time{}
			peer.LastError = fmt.Errorf("peer disconnected: no longer in active peer list")
		}
	}
}

// extractPeerID extracts the peer ID from a multiaddr string.
func extractPeerID(multiaddr string) string {
	const p2pPrefix = "/p2p/"
	for i := len(multiaddr) - len(p2pPrefix); i >= 0; i-- {
		if multiaddr[i:i+len(p2pPrefix)] == p2pPrefix {
			return multiaddr[i+len(p2pPrefix):]
		}
	}
	return ""
}

// reconnectDisconnectedPeers attempts to reconnect to all disconnected or failed peers.
func (nh *NetworkHandler) reconnectDisconnectedPeers() {
	nh.peersMu.RLock()
	disconnectedPeers := []string{}
	for addr, state := range nh.peers {
		if state.State == StateDisconnected || state.State == StateFailed {
			disconnectedPeers = append(disconnectedPeers, addr)
		}
	}
	nh.peersMu.RUnlock()
	if len(disconnectedPeers) == 0 {
		return
	}
	logger.Sugar.Debugf("Attempting to reconnect to %d disconnected peers", len(disconnectedPeers))
	for _, peerAddr := range disconnectedPeers {
		nh.wg.Add(1)
		go func(addr string) {
			defer nh.wg.Done()
			nh.attemptReconnect(addr)
		}(peerAddr)
	}
}

// attemptReconnect attempts to reconnect to a single peer.
func (nh *NetworkHandler) attemptReconnect(peerAddr string) {
	nh.peersMu.Lock()
	defer nh.peersMu.Unlock()
	peer, exists := nh.peers[peerAddr]
	if !exists {
		return
	}
	if peer.State == StateConnected || peer.State == StateConnecting || peer.State == StateReconnecting {
		return
	}
	peer.State = StateReconnecting
	if err := nh.connectWithRetryLocked(peerAddr); err != nil {
		logger.Sugar.Warnf("Reconnection to peer %s failed: %v", peerAddr, err)
	}
}

// AddPeer adds a new peer at runtime.
func (nh *NetworkHandler) AddPeer(peerAddr string) error {
	nh.peersMu.Lock()
	defer nh.peersMu.Unlock()
	if _, exists := nh.peers[peerAddr]; exists {
		return fmt.Errorf("peer already exists: %s", peerAddr)
	}
	nh.peers[peerAddr] = &PeerState{
		Address: peerAddr,
		State:   StateDisconnected,
	}
	logger.Sugar.Infof("Added new peer: %s", peerAddr)
	if nh.networkActive {
		nh.wg.Go(func() {
			nh.attemptReconnect(peerAddr)
		})
	}
	return nil
}

// RemovePeer removes a peer from the handler.
func (nh *NetworkHandler) RemovePeer(peerAddr string) error {
	nh.peersMu.Lock()
	defer nh.peersMu.Unlock()
	if _, exists := nh.peers[peerAddr]; !exists {
		return fmt.Errorf("peer not found: %s", peerAddr)
	}
	delete(nh.peers, peerAddr)
	logger.Sugar.Infof("Removed peer: %s", peerAddr)
	return nil
}

// GetPeers returns a copy of all peer states.
func (nh *NetworkHandler) GetPeers() map[string]PeerState {
	nh.peersMu.RLock()
	defer nh.peersMu.RUnlock()
	result := make(map[string]PeerState)
	for k, v := range nh.peers {
		result[k] = v.Copy()
	}
	return result
}

// GetConnectedPeers returns a list of connected peer addresses.
func (nh *NetworkHandler) GetConnectedPeers() []string {
	nh.peersMu.RLock()
	defer nh.peersMu.RUnlock()
	connected := []string{}
	for addr, state := range nh.peers {
		if state.State == StateConnected {
			connected = append(connected, addr)
		}
	}
	return connected
}

// GetPeerState returns the state of a specific peer.
func (nh *NetworkHandler) GetPeerState(peerAddr string) (PeerState, bool) {
	nh.peersMu.RLock()
	defer nh.peersMu.RUnlock()
	if peer, exists := nh.peers[peerAddr]; exists {
		return peer.Copy(), true
	}
	return PeerState{}, false
}

// GetConnectionStats returns connection statistics.
func (nh *NetworkHandler) GetConnectionStats() ConnectionStats {
	nh.peersMu.RLock()
	defer nh.peersMu.RUnlock()
	stats := ConnectionStats{
		TotalPeers:    len(nh.peers),
		NetworkActive: nh.networkActive,
		HostRunning:   nh.hostRunning,
	}
	for _, peer := range nh.peers {
		switch peer.State {
		case StateConnected:
			stats.ConnectedPeers++
		case StateConnecting, StateReconnecting:
			stats.ConnectingPeers++
		case StateFailed:
			stats.FailedPeers++
		case StateDisconnected:
			stats.DisconnectedPeers++
		}
	}
	return stats
}

// ConnectionStats provides summary statistics about peer connections.
type ConnectionStats struct {
	TotalPeers        int  `json:"total_peers"`
	ConnectedPeers    int  `json:"connected_peers"`
	ConnectingPeers   int  `json:"connecting_peers"`
	DisconnectedPeers int  `json:"disconnected_peers"`
	FailedPeers       int  `json:"failed_peers"`
	NetworkActive     bool `json:"network_active"`
	HostRunning       bool `json:"host_running"`
}
