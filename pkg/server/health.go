package server

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
)

//go:embed health_status_page.html
var embeddedHealthStatusPageHTML string

var (
	healthStatusPagePath = filepath.Join("pkg", "server", "health_status_page.html")
)

// HealthServer provides HTTP endpoints for health checks and metrics
type HealthServer struct {
	server   *http.Server
	indexer  HealthChecker
	defraURL string
}

// HealthChecker interface for checking indexer health
type HealthChecker interface {
	IsHealthy() bool
	GetCurrentBlock() int64
	GetLastProcessedTime() time.Time
	GetPeerInfo() (*P2PInfo, error)
	SignMessages(message string) (DefraPKRegistration, PeerIDRegistration, error)
}

// P2PInfo represents DefraDB P2P network information
type P2PInfo struct {
	Enabled  bool       `json:"enabled"`
	PeerInfo []PeerInfo `json:"peers"`
}

type PeerInfo struct {
	ID        string   `json:"id"`
	Addresses []string `json:"addresses"`
	PublicKey string   `json:"public_key,omitempty"`
}

type DisplayRegistration struct {
	Enabled             bool                `json:"enabled"`
	Message             string              `json:"message"`
	DefraPKRegistration DefraPKRegistration `json:"defra_pk_registration,omitempty"`
	PeerIDRegistration  PeerIDRegistration  `json:"peer_id_registration,omitempty"`
}

type DefraPKRegistration struct {
	PublicKey   string `json:"public_key,omitempty"`
	SignedPKMsg string `json:"signed_pk_message,omitempty"`
}

type PeerIDRegistration struct {
	PeerID        string `json:"peer_id,omitempty"`
	SignedPeerMsg string `json:"signed_peer_message,omitempty"`
}

// HealthResponse represents the health check response
type HealthResponse struct {
	Status           string               `json:"status"`
	Timestamp        time.Time            `json:"timestamp"`
	CurrentBlock     int64                `json:"current_block,omitempty"`
	LastProcessed    time.Time            `json:"last_processed,omitempty"`
	DefraDBConnected bool                 `json:"defradb_connected"`
	Uptime           string               `json:"uptime"`
	P2P              *P2PInfo             `json:"p2p,omitempty"`
	Registration     *DisplayRegistration `json:"registration,omitempty"`
	BuildTags        string               `json:"build_tags,omitempty"`
	SchemaType       string               `json:"schema_type,omitempty"`
}

// MetricsResponse represents basic metrics
type MetricsResponse struct {
	BlocksProcessed   int64     `json:"blocks_processed"`
	CurrentBlock      int64     `json:"current_block"`
	LastProcessedTime time.Time `json:"last_processed_time"`
	Uptime            string    `json:"uptime"`
}

var startTime = time.Now()

// NewHealthServer creates a new health server
func NewHealthServer(port int, indexer HealthChecker, defraURL string) *HealthServer {
	mux := http.NewServeMux()

	hs := &HealthServer{
		server: &http.Server{
			Addr:         fmt.Sprintf(":%d", port),
			Handler:      mux,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
		},
		indexer:  indexer,
		defraURL: defraURL,
	}

	// Register routes
	mux.HandleFunc("/health", hs.healthHandler)
	mux.HandleFunc("/registration", hs.registrationHandler)
	mux.HandleFunc("/registration-app", hs.registrationAppHandler)
	mux.HandleFunc("/metrics", hs.metricsHandler)
	mux.HandleFunc("/", hs.rootHandler)

	return hs
}

// Start starts the health server
func (hs *HealthServer) Start() error {
	logger.Sugar.Infof("Starting health server on %s", hs.server.Addr)
	return hs.server.ListenAndServe()
}

// Stop gracefully stops the health server
func (hs *HealthServer) Stop(ctx context.Context) error {
	logger.Sugar.Info("Stopping health server...")
	return hs.server.Shutdown(ctx)
}

// healthHandler handles liveness probe requests
func (hs *HealthServer) healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Content negotiation: Default to HTML for browsers, only serve JSON if explicitly requested
	accept := r.Header.Get("Accept")
	acceptLower := strings.ToLower(accept)

	// Serve JSON only if explicitly requested (Accept contains application/json and not text/html)
	// Otherwise, default to HTML for browser requests
	if strings.Contains(acceptLower, "text/html") && !strings.Contains(acceptLower, "application/json") {
		// Default to HTML (browser request or Accept header includes text/html)
		htmlContent := hs.getHealthStatusPageHTML()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(htmlContent)
		return
	}
	// Serve JSON response
	response := HealthResponse{
		Status:           "healthy",
		Timestamp:        time.Now(),
		DefraDBConnected: hs.checkDefraDB(),
		Uptime:           time.Since(startTime).String(),
		BuildTags:        getBuildTags(),
		SchemaType:       getSchemaType(),
	}

	if hs.indexer != nil {
		response.CurrentBlock = hs.indexer.GetCurrentBlock()
		response.LastProcessed = hs.indexer.GetLastProcessedTime()
		p2p, err := hs.indexer.GetPeerInfo()
		response.P2P = p2p
		if err != nil {
			response.Status = "unhealthy"
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		if !hs.indexer.IsHealthy() {
			response.Status = "unhealthy"
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// getRegistrationData returns the signed registration data for the indexer
func (hs *HealthServer) getRegistrationData() (*DisplayRegistration, error) {
	if hs.indexer == nil {
		return nil, fmt.Errorf("indexer not available")
	}

	const registrationMessage = "Shinzo Network Indexer registration"
	defraReg, peerReg, signErr := hs.indexer.SignMessages(registrationMessage)
	registration := &DisplayRegistration{
		Enabled: signErr == nil,
		Message: normalizeHex(hex.EncodeToString([]byte(registrationMessage))),
	}
	if signErr != nil {
		return registration, signErr
	}

	// Normalize signed fields to 0x-prefixed hex strings for API consumers.
	registration.DefraPKRegistration = DefraPKRegistration{
		PublicKey:   normalizeHex(defraReg.PublicKey),
		SignedPKMsg: normalizeHex(defraReg.SignedPKMsg),
	}
	registration.PeerIDRegistration = PeerIDRegistration{
		PeerID:        normalizeHex(peerReg.PeerID),
		SignedPeerMsg: normalizeHex(peerReg.SignedPeerMsg),
	}

	return registration, nil
}

// registrationHandler handles readiness probe requests
func (hs *HealthServer) registrationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if indexer is ready (has processed at least one block recently)
	ready := true
	if hs.indexer != nil {
		lastProcessed := hs.indexer.GetLastProcessedTime()
		if time.Since(lastProcessed) > 5*time.Minute && !lastProcessed.IsZero() {
			ready = false
		}
	}

	// Check DefraDB connectivity
	if !hs.checkDefraDB() {
		ready = false
	}

	response := HealthResponse{
		Status:           "ready",
		Timestamp:        time.Now(),
		DefraDBConnected: hs.checkDefraDB(),
		Uptime:           time.Since(startTime).String(),
	}

	if hs.indexer != nil {
		response.CurrentBlock = hs.indexer.GetCurrentBlock()
		response.LastProcessed = hs.indexer.GetLastProcessedTime()
		p2p, err := hs.indexer.GetPeerInfo()
		response.P2P = p2p
		if err != nil {
			response.Status = "unhealthy"
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		registration, _ := hs.getRegistrationData()
		response.Registration = registration
	}

	if !ready {
		response.Status = "not ready"
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// registrationAppHandler redirects to the registration app with registration data as query params
func (hs *HealthServer) registrationAppHandler(w http.ResponseWriter, r *http.Request) {
	registration, err := hs.getRegistrationData()
	if err != nil || registration == nil || !registration.Enabled {
		http.Error(w, "Registration data not available", http.StatusServiceUnavailable)
		return
	}

	redirectURL := fmt.Sprintf(
		"https://register.shinzo.network/?role=indexer&signedMessage=%s&peerId=%s&peerSignedMessage=%s&defraPublicKey=%s&defraPublicKeySignedMessage=%s",
		registration.Message,
		registration.PeerIDRegistration.PeerID,
		registration.PeerIDRegistration.SignedPeerMsg,
		registration.DefraPKRegistration.PublicKey,
		registration.DefraPKRegistration.SignedPKMsg,
	)

	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

// metricsHandler provides basic metrics in JSON format
func (hs *HealthServer) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	metrics := MetricsResponse{
		Uptime: time.Since(startTime).String(),
	}

	if hs.indexer != nil {
		metrics.CurrentBlock = hs.indexer.GetCurrentBlock()
		metrics.LastProcessedTime = hs.indexer.GetLastProcessedTime()
		metrics.BlocksProcessed = hs.indexer.GetCurrentBlock() // Simplified
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// rootHandler handles root requests
func (hs *HealthServer) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	response := map[string]interface{}{
		"service":   "Shinzo Network Indexer",
		"version":   "1.0.0",
		"status":    "running",
		"timestamp": time.Now(),
		"endpoints": []string{
			"/health 	   - Health probe",
			"/registration - Registration information",
			"/metrics 	   - Basic metrics",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// checkDefraDB checks if DefraDB is accessible
func (hs *HealthServer) checkDefraDB() bool {
	if hs.defraURL == "" {
		return true // Embedded mode, assume healthy
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(hs.defraURL + "/api/v0/graphql")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest // GraphQL endpoint returns 400 for GET
}

// normalizeHex ensures a string is represented as a 0x-prefixed hex string.
// If the string is empty, it is returned unchanged.
func normalizeHex(s string) string {
	if s == "" {
		return s
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		// Normalize any 0X to 0x for consistency.
		return "0x" + s[2:]
	}
	return "0x" + s
}

// getBuildTags returns the build tags used to compile the binary
func getBuildTags() string {
	if schema.IsBranchable() {
		return "branchable"
	}
	return "standard"
}

// getSchemaType returns the schema type based on build tags
func getSchemaType() string {
	if schema.IsBranchable() {
		return "branchable"
	}
	return "non-branchable"
}

// getHealthStatusPageHTML reads the HTML file from disk at runtime, falling back to embedded version
// This allows hot-reloading during development without rebuilding
func (hs *HealthServer) getHealthStatusPageHTML() []byte {
	// Try to read from disk first (for development hot-reload)
	// Check multiple possible paths relative to where the binary might be running
	possiblePaths := []string{
		healthStatusPagePath,                          // pkg/server/health_status_page.html
		filepath.Join(".", "health_status_page.html"), // ./health_status_page.html (if running from pkg/server)
	}

	for _, path := range possiblePaths {
		if data, err := os.ReadFile(path); err == nil {
			logger.Sugar.Debugf("Loaded health status page from: %s", path)
			return data
		}
	}

	// Fallback to embedded version (for production or if file not found)
	logger.Sugar.Debug("Using embedded health status page")
	return []byte(embeddedHealthStatusPageHTML)
}