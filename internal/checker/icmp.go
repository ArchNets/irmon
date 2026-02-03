package checker

import (
	"context"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// ICMPChecker performs ICMP echo (ping) checks
// Note: This is informational only and should not be decisive for health
type ICMPChecker struct {
	timeout time.Duration
}

// NewICMPChecker creates a new ICMP checker
func NewICMPChecker(timeout time.Duration) *ICMPChecker {
	return &ICMPChecker{
		timeout: timeout,
	}
}

// Name returns the protocol name
func (c *ICMPChecker) Name() string {
	return "icmp"
}

// Check performs an ICMP echo request to the target
func (c *ICMPChecker) Check(ctx context.Context, target string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Protocol:  c.Name(),
		Endpoint:  target,
		Timestamp: start,
	}

	// Create ICMP connection
	// Requires CAP_NET_RAW capability or root privileges
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		// Fallback to unprivileged mode using UDP
		return c.checkUnprivileged(ctx, target, start)
	}
	defer conn.Close()

	// Resolve target address
	dst, err := net.ResolveIPAddr("ip4", target)
	if err != nil {
		result.Error = err.Error()
		result.FailureType = FailureDNS
		return result
	}

	// Create timeout context
	deadline, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Set deadline on connection
	if d, ok := deadline.Deadline(); ok {
		conn.SetDeadline(d)
	}

	// Build ICMP echo request
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   os.Getpid() & 0xffff,
			Seq:  1,
			Data: []byte("IRMON-HEALTH-CHECK"),
		},
	}

	msgBytes, err := msg.Marshal(nil)
	if err != nil {
		result.Error = err.Error()
		result.FailureType = FailureUnknown
		return result
	}

	// Send ICMP packet
	if _, err := conn.WriteTo(msgBytes, dst); err != nil {
		result.Error = err.Error()
		result.FailureType = ClassifyError(err)
		return result
	}

	// Wait for reply
	reply := make([]byte, 1500)
	n, _, err := conn.ReadFrom(reply)
	if err != nil {
		result.Error = err.Error()
		result.FailureType = ClassifyError(err)
		return result
	}

	// Parse reply
	rm, err := icmp.ParseMessage(1, reply[:n]) // Protocol 1 = ICMP
	if err != nil {
		result.Error = err.Error()
		result.FailureType = FailurePayload
		return result
	}

	// Check if it's an echo reply
	if rm.Type == ipv4.ICMPTypeEchoReply {
		result.Success = true
		result.RTT = time.Since(start)
	} else {
		result.Error = "unexpected ICMP response type"
		result.FailureType = FailurePayload
	}

	return result
}

// checkUnprivileged performs a TCP-based connectivity check as fallback
// when ICMP is not available due to permissions
func (c *ICMPChecker) checkUnprivileged(ctx context.Context, target string, start time.Time) CheckResult {
	result := CheckResult{
		Protocol:  c.Name(),
		Endpoint:  target,
		Timestamp: start,
	}

	// Try to resolve the address as a basic connectivity test
	deadline, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var d net.Dialer
	// Try connecting to a common port (443) to check basic reachability
	conn, err := d.DialContext(deadline, "tcp", net.JoinHostPort(target, "443"))
	if err != nil {
		// This is expected - we're just checking if we can reach the host
		// Even a connection refused means the host is reachable
		if contains(err.Error(), "connection refused") {
			result.Success = true
			result.RTT = time.Since(start)
			return result
		}
		result.Error = err.Error()
		result.FailureType = ClassifyError(err)
		return result
	}
	conn.Close()

	result.Success = true
	result.RTT = time.Since(start)
	return result
}
