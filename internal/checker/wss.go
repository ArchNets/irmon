package checker

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// WSSChecker performs Secure WebSocket (WSS) connection checks
type WSSChecker struct {
	timeout   time.Duration
	tlsConfig *tls.Config
	dialer    *websocket.Dialer
}

// NewWSSChecker creates a new Secure WebSocket checker
func NewWSSChecker(timeout time.Duration, skipVerify bool) *WSSChecker {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: skipVerify,
		MinVersion:         tls.VersionTLS12,
	}

	return &WSSChecker{
		timeout:   timeout,
		tlsConfig: tlsConfig,
		dialer: &websocket.Dialer{
			HandshakeTimeout: timeout,
			TLSClientConfig:  tlsConfig,
		},
	}
}

// Name returns the protocol name
func (c *WSSChecker) Name() string {
	return "wss"
}

// Check performs a Secure WebSocket connection and handshake test
// Endpoint format: wss://host:port/path (e.g., "wss://tunnel.example.com:443/health")
func (c *WSSChecker) Check(ctx context.Context, endpoint string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Protocol:  c.Name(),
		Endpoint:  endpoint,
		Timestamp: start,
	}

	// Create timeout context
	deadline, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Set headers to appear as normal browser traffic
	headers := http.Header{}
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	headers.Set("Origin", "https://www.google.com")

	// Attempt WSS connection
	conn, resp, err := c.dialer.DialContext(deadline, endpoint, headers)
	if err != nil {
		result.Error = err.Error()
		result.FailureType = classifyWSSError(err, resp)
		return result
	}
	defer conn.Close()

	// The TLS handshake completed successfully at this point
	// Send a minimal ping to verify the connection is truly functional
	if err := conn.WriteMessage(websocket.PingMessage, []byte("health")); err != nil {
		result.Error = fmt.Sprintf("ping failed: %v", err)
		result.FailureType = FailurePayload
		return result
	}

	// Set read deadline for pong
	conn.SetReadDeadline(time.Now().Add(c.timeout / 2))

	// Try to read a response
	_, _, err = conn.ReadMessage()
	if err != nil {
		// Timeout on read is acceptable - TLS connection was established
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) ||
			contains(err.Error(), "timeout") {
			result.Success = true
			result.RTT = time.Since(start)
			return result
		}
		result.Error = fmt.Sprintf("read failed: %v", err)
		result.FailureType = ClassifyError(err)
		return result
	}

	result.Success = true
	result.RTT = time.Since(start)
	return result
}

// classifyWSSError categorizes WSS-specific errors
func classifyWSSError(err error, resp *http.Response) FailureType {
	errStr := err.Error()

	// Check for TLS-specific errors first
	if contains(errStr, "tls:", "certificate", "x509:", "handshake") {
		return FailureTLS
	}

	if resp != nil {
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusUpgradeRequired:
			return FailureHandshake
		case http.StatusForbidden, http.StatusUnauthorized:
			return FailureHandshake
		case http.StatusBadGateway, http.StatusServiceUnavailable:
			return FailureConnect
		}
	}

	return ClassifyError(err)
}
