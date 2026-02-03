package state

import (
	"sync"
	"time"

	"github.com/archnet/irmon/internal/scoring"
)

// ScoreEntry represents a historical score entry
type ScoreEntry struct {
	Score     int                 `json:"score"`
	State     scoring.ServerState `json:"state"`
	Timestamp time.Time           `json:"timestamp"`
}

// ServerInfo holds the current state and history for a server
type ServerInfo struct {
	Name                  string              `json:"name"`
	OriginIP              string              `json:"origin_ip"`
	CurrentState          scoring.ServerState `json:"current_state"`
	CurrentScore          int                 `json:"current_score"`
	CurrentWeight         int                 `json:"current_weight"`
	ConsecutiveFailures   int                 `json:"consecutive_failures"`
	ConsecutiveRecoveries int                 `json:"consecutive_recoveries"`
	LastStateChange       time.Time           `json:"last_state_change"`
	LastCheck             time.Time           `json:"last_check"`
	History               []ScoreEntry        `json:"history"`

	// Bandwidth metrics
	BandwidthRxBytes       uint64    `json:"bandwidth_rx_bytes"`
	BandwidthTxBytes       uint64    `json:"bandwidth_tx_bytes"`
	BandwidthRxBytesPerSec uint64    `json:"bandwidth_rx_bytes_per_sec"`
	BandwidthTxBytesPerSec uint64    `json:"bandwidth_tx_bytes_per_sec"`
	LastBandwidthUpdate    time.Time `json:"last_bandwidth_update"`
}

// Manager manages server state with hysteresis and history
type Manager struct {
	mu sync.RWMutex

	servers map[string]*ServerInfo

	// Configuration
	failureThreshold  int           // Consecutive failures before state change
	recoveryThreshold int           // Consecutive successes before recovery
	flappingWindow    time.Duration // Cooldown after state change
	historySize       int           // Number of history entries to keep
	degradedThreshold int           // Score threshold for degraded state
}

// NewManager creates a new state manager
func NewManager(failureThreshold, recoveryThreshold int, flappingWindow time.Duration, degradedThreshold int) *Manager {
	return &Manager{
		servers:           make(map[string]*ServerInfo),
		failureThreshold:  failureThreshold,
		recoveryThreshold: recoveryThreshold,
		flappingWindow:    flappingWindow,
		historySize:       100, // Keep last 100 entries
		degradedThreshold: degradedThreshold,
	}
}

// RegisterServer adds a new server to be tracked
func (m *Manager) RegisterServer(name, originIP string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.servers[name]; !exists {
		m.servers[name] = &ServerInfo{
			Name:            name,
			OriginIP:        originIP,
			CurrentState:    scoring.StateFullyUsable, // Start optimistic
			LastStateChange: time.Now(),
			History:         make([]ScoreEntry, 0, m.historySize),
		}
	}
}

// StateChange represents a state transition
type StateChange struct {
	ServerName string
	OldState   scoring.ServerState
	NewState   scoring.ServerState
	OldWeight  int
	NewWeight  int
	Reason     string
}

// Update processes a new score result and returns any state change
func (m *Manager) Update(result scoring.ScoreResult, weight int) *StateChange {
	m.mu.Lock()
	defer m.mu.Unlock()

	server, exists := m.servers[result.ServerName]
	if !exists {
		return nil
	}

	now := time.Now()
	oldState := server.CurrentState
	oldWeight := server.CurrentWeight

	// Add to history
	entry := ScoreEntry{
		Score:     result.TotalScore,
		State:     result.State,
		Timestamp: now,
	}
	server.History = append(server.History, entry)
	if len(server.History) > m.historySize {
		server.History = server.History[1:]
	}

	// Update current values
	server.CurrentScore = result.TotalScore
	server.CurrentWeight = weight
	server.LastCheck = now

	// Check for state transition with hysteresis
	newState := m.evaluateStateTransition(server, result)

	if newState != oldState {
		server.CurrentState = newState
		server.LastStateChange = now
		server.ConsecutiveFailures = 0
		server.ConsecutiveRecoveries = 0

		return &StateChange{
			ServerName: result.ServerName,
			OldState:   oldState,
			NewState:   newState,
			OldWeight:  oldWeight,
			NewWeight:  weight,
			Reason:     m.getChangeReason(oldState, newState, result),
		}
	}

	return nil
}

// evaluateStateTransition determines if a state should change with hysteresis
func (m *Manager) evaluateStateTransition(server *ServerInfo, result scoring.ScoreResult) scoring.ServerState {
	now := time.Now()
	desiredState := result.State

	// Check flapping protection
	if now.Sub(server.LastStateChange) < m.flappingWindow {
		// Within cooldown, don't change state
		return server.CurrentState
	}

	// If score is healthy but we're in a bad state, track recovery
	if desiredState > server.CurrentState {
		server.ConsecutiveRecoveries++
		server.ConsecutiveFailures = 0

		if server.ConsecutiveRecoveries >= m.recoveryThreshold {
			return desiredState
		}
		return server.CurrentState
	}

	// If score indicates degradation, track failures
	if desiredState < server.CurrentState {
		// Special rule: Never disable if any high-priority transport works
		if result.WorkingCritical && desiredState == scoring.StateIranOnly {
			// Keep at least DEGRADED if critical transport works
			server.ConsecutiveFailures = 0
			return scoring.StateDegraded
		}

		server.ConsecutiveFailures++
		server.ConsecutiveRecoveries = 0

		if server.ConsecutiveFailures >= m.failureThreshold {
			return desiredState
		}
		return server.CurrentState
	}

	// Same state, reset counters
	server.ConsecutiveFailures = 0
	server.ConsecutiveRecoveries = 0
	return server.CurrentState
}

// getChangeReason provides a human-readable reason for state change
func (m *Manager) getChangeReason(oldState, newState scoring.ServerState, result scoring.ScoreResult) string {
	if newState > oldState {
		return "score recovered above threshold"
	}
	if result.WorkingCritical {
		return "degraded but critical transport still working"
	}
	return "score dropped below threshold after consecutive failures"
}

// GetServer returns the current state info for a server
func (m *Manager) GetServer(name string) (*ServerInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	server, exists := m.servers[name]
	if !exists {
		return nil, false
	}

	// Return a copy
	copy := *server
	copy.History = make([]ScoreEntry, len(server.History))
	for i, h := range server.History {
		copy.History[i] = h
	}
	return &copy, true
}

// GetAllServers returns state info for all servers
func (m *Manager) GetAllServers() map[string]*ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ServerInfo)
	for name, server := range m.servers {
		copy := *server
		copy.History = make([]ScoreEntry, len(server.History))
		for i, h := range server.History {
			copy.History[i] = h
		}
		result[name] = &copy
	}
	return result
}

// GetServersNeedingUpdate returns servers whose CF weight needs updating
func (m *Manager) GetServersNeedingUpdate() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []ServerInfo
	for _, server := range m.servers {
		// Return all servers that have been checked recently
		if time.Since(server.LastCheck) < time.Minute {
			copy := *server
			result = append(result, copy)
		}
	}
	return result
}

// AverageScore returns the average score over the history window
func (m *Manager) AverageScore(serverName string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	server, exists := m.servers[serverName]
	if !exists || len(server.History) == 0 {
		return 0
	}

	var total int
	for _, h := range server.History {
		total += h.Score
	}
	return float64(total) / float64(len(server.History))
}

// RecentTrend returns the score trend direction (positive = improving)
func (m *Manager) RecentTrend(serverName string, sampleSize int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	server, exists := m.servers[serverName]
	if !exists || len(server.History) < 2 {
		return 0
	}

	history := server.History
	if len(history) > sampleSize {
		history = history[len(history)-sampleSize:]
	}

	if len(history) < 2 {
		return 0
	}

	// Compare first half to second half
	mid := len(history) / 2
	var firstHalf, secondHalf int
	for i := 0; i < mid; i++ {
		firstHalf += history[i].Score
	}
	for i := mid; i < len(history); i++ {
		secondHalf += history[i].Score
	}

	firstAvg := firstHalf / mid
	secondAvg := secondHalf / (len(history) - mid)

	return secondAvg - firstAvg
}

// UpdateBandwidth updates the bandwidth metrics for a server
func (m *Manager) UpdateBandwidth(serverName string, rxBytes, txBytes, rxBytesPerSec, txBytesPerSec uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	server, exists := m.servers[serverName]
	if !exists {
		return
	}

	server.BandwidthRxBytes = rxBytes
	server.BandwidthTxBytes = txBytes
	server.BandwidthRxBytesPerSec = rxBytesPerSec
	server.BandwidthTxBytesPerSec = txBytesPerSec
	server.LastBandwidthUpdate = time.Now()
}
