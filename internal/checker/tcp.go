package checker

import (
	"context"
	"net"
	"time"
)

// TCPChecker performs raw TCP connection checks
// Note: This is informational only in censored environments
type TCPChecker struct {
	timeout time.Duration
}

// NewTCPChecker creates a new TCP checker
func NewTCPChecker(timeout time.Duration) *TCPChecker {
	return &TCPChecker{
		timeout: timeout,
	}
}

// Name returns the protocol name
func (c *TCPChecker) Name() string {
	return "tcp"
}

// Check performs a TCP connection test to the target endpoint
// Endpoint format: host:port (e.g., "185.1.2.3:443")
func (c *TCPChecker) Check(ctx context.Context, endpoint string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Protocol:  c.Name(),
		Endpoint:  endpoint,
		Timestamp: start,
	}

	// Create timeout context
	deadline, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Attempt TCP connection
	var d net.Dialer
	conn, err := d.DialContext(deadline, "tcp", endpoint)
	if err != nil {
		result.Error = err.Error()
		result.FailureType = ClassifyError(err)
		return result
	}
	defer conn.Close()

	result.Success = true
	result.RTT = time.Since(start)
	return result
}
