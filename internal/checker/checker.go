package checker

import (
	"context"
	"time"
)

// FailureType categorizes the reason for a check failure
type FailureType string

const (
	FailureNone      FailureType = ""
	FailureTimeout   FailureType = "timeout"
	FailureReset     FailureType = "reset"
	FailureHandshake FailureType = "handshake"
	FailureTLS       FailureType = "tls"
	FailurePayload   FailureType = "payload"
	FailureDNS       FailureType = "dns"
	FailureConnect   FailureType = "connect"
	FailureUnknown   FailureType = "unknown"
)

// CheckResult represents the outcome of a protocol check
type CheckResult struct {
	Protocol    string        `json:"protocol"`
	Endpoint    string        `json:"endpoint"`
	Success     bool          `json:"success"`
	RTT         time.Duration `json:"rtt"`
	Error       string        `json:"error,omitempty"`
	FailureType FailureType   `json:"failure_type,omitempty"`
	Timestamp   time.Time     `json:"timestamp"`
}

// Checker is the interface that all protocol checkers must implement
type Checker interface {
	// Name returns the protocol name (e.g., "wssmux", "wss", "tcp")
	Name() string

	// Check performs a connectivity check to the given endpoint
	// The context can be used to enforce timeouts
	Check(ctx context.Context, endpoint string) CheckResult
}

// Registry maintains a collection of available checkers
type Registry struct {
	checkers map[string]Checker
}

// NewRegistry creates a new checker registry
func NewRegistry() *Registry {
	return &Registry{
		checkers: make(map[string]Checker),
	}
}

// Register adds a checker to the registry
func (r *Registry) Register(checker Checker) {
	r.checkers[checker.Name()] = checker
}

// Get retrieves a checker by name
func (r *Registry) Get(name string) (Checker, bool) {
	c, ok := r.checkers[name]
	return c, ok
}

// All returns all registered checkers
func (r *Registry) All() map[string]Checker {
	return r.checkers
}

// ClassifyError attempts to categorize a connection error into a FailureType
func ClassifyError(err error) FailureType {
	if err == nil {
		return FailureNone
	}

	errStr := err.Error()

	// Check for timeout
	if contains(errStr, "timeout", "deadline exceeded", "i/o timeout") {
		return FailureTimeout
	}

	// Check for connection reset
	if contains(errStr, "connection reset", "reset by peer", "ECONNRESET") {
		return FailureReset
	}

	// Check for TLS errors
	if contains(errStr, "tls:", "certificate", "x509:", "handshake failure") {
		return FailureTLS
	}

	// Check for DNS errors
	if contains(errStr, "no such host", "lookup", "dns") {
		return FailureDNS
	}

	// Check for connection refused
	if contains(errStr, "connection refused", "ECONNREFUSED", "connect:") {
		return FailureConnect
	}

	return FailureUnknown
}

// contains checks if the string contains any of the substrings
func contains(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
