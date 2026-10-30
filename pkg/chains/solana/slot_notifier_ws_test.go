package solana

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// In-process fake Solana pubsub server speaking the real wire protocol, so
// the production wsDialer is exercised end to end (dial → slotSubscribe →
// notifications) without network access or a build tag.
//
// Wire shapes (validated against the solana-go ws client):
//
//	request:      {"jsonrpc":"2.0","method":"slotSubscribe","id":<n>}
//	subscribe ok: {"jsonrpc":"2.0","id":<n>,"result":<subID>}
//	notification: {"jsonrpc":"2.0","method":"slotNotification",
//	               "params":{"result":{"parent":P,"root":R,"slot":S},"subscription":<subID>}}
// ---------------------------------------------------------------------------

type fakePubsubServer struct {
	srv   *http.Server
	wsURL string

	mu           sync.Mutex
	conns        []*websocket.Conn // one per dial, in dial order
	subIDs       []uint64          // assigned subscription id per connection
	methods      []string          // recorded request methods
	nextSubID    uint64
	droppedConns map[int]bool // 0-based conn indexes closed right after subscribing
}

func newFakePubsubServer(t *testing.T) *fakePubsubServer {
	t.Helper()
	s := &fakePubsubServer{droppedConns: map[int]bool{}}
	upgrader := websocket.Upgrader{}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s.wsURL = "ws://" + ln.Addr().String()

	s.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		s.mu.Lock()
		connIdx := len(s.conns)
		drop := s.droppedConns[connIdx]
		s.conns = append(s.conns, conn)
		s.mu.Unlock()

		defer func() { _ = conn.Close() }()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(msg, &req) != nil || req.Method != "slotSubscribe" {
				continue
			}
			s.mu.Lock()
			s.nextSubID++
			subID := s.nextSubID
			s.subIDs = append(s.subIDs, subID)
			s.methods = append(s.methods, req.Method)
			s.mu.Unlock()

			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage,
				[]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%d}`, req.ID, subID))); err != nil {
				return
			}
			if drop {
				// Simulate the endpoint dropping the connection right after
				// the subscription is confirmed: the client must detect the
				// dead stream and reconnect.
				_ = conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseGoingAway, "drop"))
				return
			}
		}
	})}
	go func() { _ = s.srv.Serve(ln) }()
	t.Cleanup(func() { _ = s.srv.Close() })
	return s
}

// dropAfterSubscribe marks a not-yet-accepted connection to be closed by the
// server immediately after it confirms its slotSubscribe.
func (s *fakePubsubServer) dropAfterSubscribe(connIdx int) {
	s.mu.Lock()
	s.droppedConns[connIdx] = true
	s.mu.Unlock()
}

// push sends one slotNotification to the given connection (0-based).
func (s *fakePubsubServer) push(connIdx int, slot uint64) {
	s.mu.Lock()
	conn := s.conns[connIdx]
	subID := s.subIDs[connIdx]
	s.mu.Unlock()

	payload := fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"slotNotification","params":{"result":{"parent":%d,"root":%d,"slot":%d},"subscription":%d}}`,
		slot-1, 0, slot, subID)
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(payload))
}

func (s *fakePubsubServer) subscribeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.methods)
}

func (s *fakePubsubServer) recordedMethods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

func TestSlotNotifierWS_EndToEnd(t *testing.T) {
	server := newFakePubsubServer(t)
	n := newSlotNotifier(server.wsURL, "", "")
	n.start(context.Background())
	t.Cleanup(n.stop)

	waitForCondition(t, 5*time.Second, func() bool { return server.subscribeCalls() == 1 },
		"notifier must dial and slotSubscribe the fake endpoint")
	assert.Equal(t, []string{"slotSubscribe"}, server.recordedMethods())

	server.push(0, 500)
	awaitHintDone(t, n, 500, 5*time.Second)
}

func TestSlotNotifierWS_ReconnectAfterServerDrop(t *testing.T) {
	// Sequential: mutates the backoff seam var.
	old := reconnectBackoffBaseVar
	reconnectBackoffBaseVar = 10 * time.Millisecond
	t.Cleanup(func() { reconnectBackoffBaseVar = old })

	server := newFakePubsubServer(t)
	server.dropAfterSubscribe(0) // first connection dies right after subscribing

	n := newSlotNotifier(server.wsURL, "", "")
	n.start(context.Background())
	t.Cleanup(n.stop)

	// The first stream is dropped by the server; the notifier must detect
	// it and dial again.
	waitForCondition(t, 5*time.Second, func() bool { return server.subscribeCalls() == 2 },
		"notifier must reconnect after the server drop")

	server.push(1, 20)
	awaitHintDone(t, n, 20, 5*time.Second)
}

func TestSlotNotifierWS_DialRefusalDegradesToBlind(t *testing.T) {
	// Sequential: mutates both seam vars.
	oldStall := hintStallTimeoutVar
	oldBase := reconnectBackoffBaseVar
	hintStallTimeoutVar = 60 * time.Millisecond
	reconnectBackoffBaseVar = 10 * time.Millisecond
	t.Cleanup(func() {
		hintStallTimeoutVar = oldStall
		reconnectBackoffBaseVar = oldBase
	})

	// A listener that is immediately closed: dials are refused, not hung.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	n := newSlotNotifier("ws://"+addr, "", "")
	n.start(context.Background())
	t.Cleanup(n.stop)

	awaitHintDone(t, n, 100, 2*time.Second)
	assert.True(t, n.stalled.Load(), "a refused endpoint must degrade to the blind path")
}
