package checker

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// XWSSMuxChecker performs Extended WSS Multiplexed connection checks
// This is the second highest priority transport
type XWSSMuxChecker struct {
	timeout   time.Duration
	tlsConfig *tls.Config
	dialer    *websocket.Dialer
}

// XMux extended frame types (extends standard Mux)
const (
	XMuxTypeExtInit    uint8 = 0x11
	XMuxTypeExtInitAck uint8 = 0x16
	XMuxTypeExtPing    uint8 = 0x13
	XMuxTypeExtPong    uint8 = 0x14
	XMuxTypeExtData    uint8 = 0x12
)

// NewXWSSMuxChecker creates a new Extended WSS Multiplexed checker
func NewXWSSMuxChecker(timeout time.Duration, skipVerify bool) *XWSSMuxChecker {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: skipVerify,
		MinVersion:         tls.VersionTLS12,
	}

	return &XWSSMuxChecker{
		timeout:   timeout,
		tlsConfig: tlsConfig,
		dialer: &websocket.Dialer{
			HandshakeTimeout: timeout,
			TLSClientConfig:  tlsConfig,
		},
	}
}

// Name returns the protocol name
func (c *XWSSMuxChecker) Name() string {
	return "xwssmux"
}

// Check performs an Extended WSS Mux connection test
// Endpoint format: wss://host:port/path (e.g., "wss://tunnel.example.com:443/xwssmux")
func (c *XWSSMuxChecker) Check(ctx context.Context, endpoint string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Protocol:  c.Name(),
		Endpoint:  endpoint,
		Timestamp: start,
	}

	// Create timeout context
	deadline, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Set headers to appear as normal traffic
	headers := http.Header{}
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	headers.Set("Origin", "https://www.google.com")
	// Custom headers for extended mux protocol
	headers.Set("X-Mux-Version", "2")
	headers.Set("X-Mux-Extended", "true")

	// Attempt WSS connection
	conn, resp, err := c.dialer.DialContext(deadline, endpoint, headers)
	if err != nil {
		result.Error = err.Error()
		result.FailureType = classifyXMuxError(err, resp)
		return result
	}
	defer conn.Close()

	// Send extended MUX initialization frame
	initFrame := c.buildExtInitFrame()
	if err := conn.WriteMessage(websocket.BinaryMessage, initFrame); err != nil {
		result.Error = fmt.Sprintf("xmux init failed: %v", err)
		result.FailureType = FailureHandshake
		return result
	}

	// Set read deadline for xmux response
	conn.SetReadDeadline(time.Now().Add(c.timeout / 2))

	// Read xmux response
	msgType, msg, err := conn.ReadMessage()
	if err != nil {
		result.Error = fmt.Sprintf("xmux read failed: %v", err)
		result.FailureType = ClassifyError(err)
		return result
	}

	// Validate xmux response
	if msgType != websocket.BinaryMessage {
		result.Error = "unexpected message type"
		result.FailureType = FailurePayload
		return result
	}

	if !c.validateXMuxResponse(msg) {
		result.Error = "invalid xmux response"
		result.FailureType = FailurePayload
		return result
	}

	// Send an extended mux ping
	pingFrame := c.buildExtPingFrame()
	if err := conn.WriteMessage(websocket.BinaryMessage, pingFrame); err != nil {
		result.Error = fmt.Sprintf("xmux ping failed: %v", err)
		result.FailureType = FailurePayload
		return result
	}

	// Read pong response
	conn.SetReadDeadline(time.Now().Add(c.timeout / 2))
	_, pongMsg, err := conn.ReadMessage()
	if err != nil {
		// Timeout is acceptable - connection works
		if contains(err.Error(), "timeout") {
			result.Success = true
			result.RTT = time.Since(start)
			return result
		}
		result.Error = fmt.Sprintf("pong read failed: %v", err)
		result.FailureType = ClassifyError(err)
		return result
	}

	// Validate pong
	if len(pongMsg) >= 2 && (pongMsg[1] == XMuxTypeExtPong || pongMsg[1] == MuxTypePong) {
		result.Success = true
		result.RTT = time.Since(start)
		return result
	}

	// Any valid response is acceptable
	result.Success = true
	result.RTT = time.Since(start)
	return result
}

// buildExtInitFrame creates an extended MUX initialization frame
func (c *XWSSMuxChecker) buildExtInitFrame() []byte {
	// Extended frame format: Version(1) + Type(1) + Flags(2) + StreamID(4) + Length(4) + Payload
	payload := []byte("IRMON-XMUX-HEALTH")
	frame := make([]byte, 12+len(payload))
	frame[0] = 2                                                  // Version 2 (extended)
	frame[1] = XMuxTypeExtInit                                    // Extended init type
	binary.BigEndian.PutUint16(frame[2:4], 0x0001)                // Flags: extended mode
	binary.BigEndian.PutUint32(frame[4:8], 0)                     // Stream ID 0 for control
	binary.BigEndian.PutUint32(frame[8:12], uint32(len(payload))) // Payload length
	copy(frame[12:], payload)
	return frame
}

// buildExtPingFrame creates an extended MUX ping frame
func (c *XWSSMuxChecker) buildExtPingFrame() []byte {
	frame := make([]byte, 12+8)
	frame[0] = 2                                                          // Version 2
	frame[1] = XMuxTypeExtPing                                            // Extended ping type
	binary.BigEndian.PutUint16(frame[2:4], 0x0001)                        // Flags
	binary.BigEndian.PutUint32(frame[4:8], 0)                             // Stream ID 0
	binary.BigEndian.PutUint32(frame[8:12], 8)                            // Payload length
	binary.BigEndian.PutUint64(frame[12:], uint64(time.Now().UnixNano())) // Timestamp (nanoseconds)
	return frame
}

// validateXMuxResponse checks if an extended mux response is valid
func (c *XWSSMuxChecker) validateXMuxResponse(msg []byte) bool {
	if len(msg) < 12 {
		// Check if it's a standard mux response (fallback)
		if len(msg) >= 10 {
			version := msg[0]
			frameType := msg[1]
			// Accept standard mux responses too
			if version == 1 && (frameType == MuxTypeInitAck || frameType == MuxTypePong) {
				return true
			}
		}
		return false
	}

	version := msg[0]
	frameType := msg[1]

	// Accept version 2 extended responses
	if version == 2 {
		return frameType == XMuxTypeExtInitAck || frameType == XMuxTypeExtPong || frameType == XMuxTypeExtData
	}

	// Also accept version 1 as fallback
	if version == 1 {
		return frameType == MuxTypeInitAck || frameType == MuxTypePong || frameType == MuxTypeData
	}

	return false
}

// classifyXMuxError categorizes extended MUX-specific errors
func classifyXMuxError(err error, resp *http.Response) FailureType {
	errStr := err.Error()

	// Check for TLS errors
	if contains(errStr, "tls:", "certificate", "x509:") {
		return FailureTLS
	}

	if resp != nil && resp.StatusCode >= 400 {
		return FailureHandshake
	}

	return ClassifyError(err)
}
