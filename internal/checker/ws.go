package checker

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// WSChecker performs WebSocket (WS) connection checks
type WSChecker struct {
	timeout time.Duration
	dialer  *websocket.Dialer
}

// NewWSChecker creates a new WebSocket checker
func NewWSChecker(timeout time.Duration) *WSChecker {
	return &WSChecker{
		timeout: timeout,
		dialer: &websocket.Dialer{
			HandshakeTimeout: timeout,
		},
	}
}

// Name returns the protocol name
func (c *WSChecker) Name() string {
	return "ws"
}

// Check performs a WebSocket connection and handshake test
// Endpoint format: ws://host:port/path (e.g., "ws://tunnel.example.com:80/health")
func (c *WSChecker) Check(ctx context.Context, endpoint string) CheckResult {
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

	// Attempt WebSocket connection
	conn, resp, err := c.dialer.DialContext(deadline, endpoint, headers)
	if err != nil {
		result.Error = err.Error()
		result.FailureType = classifyWSError(err, resp)
		return result
	}
	defer conn.Close()

	// Send a minimal ping to verify the connection is truly functional
	if err := conn.WriteMessage(websocket.PingMessage, []byte("health")); err != nil {
		result.Error = fmt.Sprintf("ping failed: %v", err)
		result.FailureType = FailurePayload
		return result
	}

	// Set read deadline for pong
	conn.SetReadDeadline(time.Now().Add(c.timeout / 2))

	// Try to read a response (may be pong or any message)
	_, _, err = conn.ReadMessage()
	if err != nil {
		// Timeout on read is acceptable - connection was established
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) ||
			contains(err.Error(), "timeout") {
			result.Success = true
			result.RTT = time.Since(start)
			return result
		}
		// Other errors indicate a problem
		result.Error = fmt.Sprintf("read failed: %v", err)
		result.FailureType = ClassifyError(err)
		return result
	}

	result.Success = true
	result.RTT = time.Since(start)
	return result
}

// classifyWSError categorizes WebSocket-specific errors
func classifyWSError(err error, resp *http.Response) FailureType {
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
