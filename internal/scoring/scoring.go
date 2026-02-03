package scoring

import (
	"sync"
	"time"

	"github.com/archnet/irmon/internal/checker"
)

// ServerState represents the operational state of a server
type ServerState string

const (
	StateFullyUsable ServerState = "FULLY_USABLE" // Score >= 60
	StateDegraded    ServerState = "DEGRADED"     // Score 20-59
	StateIranOnly    ServerState = "IRAN_ONLY"    // Score < 20
)

// DefaultWeights provides the default scoring weights for each protocol
var DefaultWeights = map[string]int{
	"wssmux":  100,
	"xwssmux": 80,
	"wss":     60,
	"ws":      40,
	"tcp":     20,
	"icmp":    5,
}

// Scorer calculates server health scores based on protocol check results
type Scorer struct {
	mu         sync.RWMutex
	weights    map[string]int
	thresholds struct {
		fullyUsable int
		degraded    int
	}
}

// ScoreResult holds the complete scoring output for a server
type ScoreResult struct {
	ServerName      string                   `json:"server_name"`
	TotalScore      int                      `json:"total_score"`
	MaxPossible     int                      `json:"max_possible"`
	ProtocolScores  map[string]ProtocolScore `json:"protocol_scores"`
	State           ServerState              `json:"state"`
	WorkingCritical bool                     `json:"working_critical"` // True if any high-priority transport works
	Timestamp       time.Time                `json:"timestamp"`
}

// ProtocolScore represents the individual score contribution from a protocol
type ProtocolScore struct {
	Protocol string `json:"protocol"`
	Success  bool   `json:"success"`
	Score    int    `json:"score"`
	Weight   int    `json:"weight"`
	RTT      int64  `json:"rtt_ms"`
}

// NewScorer creates a new scorer with the given weights and thresholds
func NewScorer(weights map[string]int, fullyUsableThreshold, degradedThreshold int) *Scorer {
	s := &Scorer{
		weights: make(map[string]int),
	}

	// Copy weights, falling back to defaults
	for proto, defaultWeight := range DefaultWeights {
		if w, ok := weights[proto]; ok {
			s.weights[proto] = w
		} else {
			s.weights[proto] = defaultWeight
		}
	}

	// Add any custom weights not in defaults
	for proto, w := range weights {
		if _, ok := s.weights[proto]; !ok {
			s.weights[proto] = w
		}
	}

	s.thresholds.fullyUsable = fullyUsableThreshold
	s.thresholds.degraded = degradedThreshold

	return s
}

// Calculate computes the server score from check results
func (s *Scorer) Calculate(serverName string, results []checker.CheckResult) ScoreResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	output := ScoreResult{
		ServerName:     serverName,
		ProtocolScores: make(map[string]ProtocolScore),
		Timestamp:      now,
	}

	// Calculate individual scores
	for _, result := range results {
		weight := s.getWeight(result.Protocol)
		output.MaxPossible += weight

		ps := ProtocolScore{
			Protocol: result.Protocol,
			Success:  result.Success,
			Weight:   weight,
			RTT:      result.RTT.Milliseconds(),
		}

		if result.Success {
			ps.Score = weight
			output.TotalScore += weight

			// Check if this is a critical transport
			if isCriticalProtocol(result.Protocol) {
				output.WorkingCritical = true
			}
		}

		output.ProtocolScores[result.Protocol] = ps
	}

	// Determine state
	output.State = s.determineState(output.TotalScore)

	return output
}

// determineState calculates the server state based on score
func (s *Scorer) determineState(score int) ServerState {
	if score >= s.thresholds.fullyUsable {
		return StateFullyUsable
	}
	if score >= s.thresholds.degraded {
		return StateDegraded
	}
	return StateIranOnly
}

// getWeight returns the weight for a protocol
func (s *Scorer) getWeight(protocol string) int {
	if w, ok := s.weights[protocol]; ok {
		return w
	}
	return 0
}

// isCriticalProtocol returns true if the protocol is high-priority
func isCriticalProtocol(protocol string) bool {
	switch protocol {
	case "wssmux", "xwssmux", "wss", "ws":
		return true
	default:
		return false
	}
}

// CalculateCloudflareWeight converts a score to a Cloudflare weight
// Weight is capped at 100, and scores below the degraded threshold result in 0
func (s *Scorer) CalculateCloudflareWeight(result ScoreResult) int {
	// If any critical transport works, ensure minimum weight
	if result.WorkingCritical && result.TotalScore < s.thresholds.degraded {
		// Keep server enabled with minimum weight if critical transport works
		return s.thresholds.degraded
	}

	// Score < 20 (Iran-only) -> weight 0
	if result.TotalScore < s.thresholds.degraded {
		return 0
	}

	// Cap at 100
	if result.TotalScore > 100 {
		return 100
	}

	return result.TotalScore
}

// GetThresholds returns the current thresholds
func (s *Scorer) GetThresholds() (fullyUsable, degraded int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.thresholds.fullyUsable, s.thresholds.degraded
}

// GetWeights returns a copy of the current weights
func (s *Scorer) GetWeights() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	copy := make(map[string]int)
	for k, v := range s.weights {
		copy[k] = v
	}
	return copy
}
