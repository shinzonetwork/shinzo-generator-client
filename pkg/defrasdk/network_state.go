package defrasdk

import (
	"time"
)

// ConnectionState represents the current state of a peer connection
type ConnectionState int

const (
	// StateDisconnected: Not connected to the peer
	StateDisconnected ConnectionState = iota
	// StateConnecting: Attempting to establish connection
	StateConnecting
	// StateConnected: Successfully connected to the peer
	StateConnected
	// StateReconnecting: Lost connection, attempting to reconnect
	StateReconnecting
	// StateFailed: Connection failed after max retries
	StateFailed
)

// String returns a human-readable representation of the connection state
func (s ConnectionState) String() string {
	switch s {
	case StateDisconnected:
		return "DISCONNECTED"
	case StateConnecting:
		return "CONNECTING"
	case StateConnected:
		return "CONNECTED"
	case StateReconnecting:
		return "RECONNECTING"
	case StateFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// PeerState tracks the connection state of an individual peer
type PeerState struct {
	Address     string          // Peer address in multiaddr format
	State       ConnectionState // Current connection state
	LastAttempt time.Time       // Time of last connection attempt
	RetryCount  int             // Number of retry attempts
	LastError   error           // Last error encountered during connection
	ConnectedAt time.Time       // Time when connection was established (zero if not connected)
}

// IsConnected returns true if the peer is currently connected
func (p *PeerState) IsConnected() bool {
	return p.State == StateConnected
}

// IsReachable returns true if the peer can potentially be reached
func (p *PeerState) IsReachable() bool {
	return p.State != StateFailed
}

// Copy returns a copy of the PeerState
func (p *PeerState) Copy() PeerState {
	return PeerState{
		Address:     p.Address,
		State:       p.State,
		LastAttempt: p.LastAttempt,
		RetryCount:  p.RetryCount,
		LastError:   p.LastError,
		ConnectedAt: p.ConnectedAt,
	}
}