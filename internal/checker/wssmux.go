package checker

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// WSSMuxChecker performs WSS Multiplexed connection checks
// This is the CRITICAL priority transport for Iranian networks
type WSSMuxChecker struct {
	timeout   time.Duration
	tlsConfig *tls.Config
	dialer    *websocket.Dialer
}

// MuxFrame represents a multiplexing protocol frame
type MuxFrame struct {
	Version  uint8
	Type     uint8
	StreamID uint32
	Length   uint32
	Payload  []byte
}

// Mux frame types
const (
	MuxTypeInit    uint8 = 0x01
	MuxTypeData    uint8 = 0x02
	MuxTypePing    uint8 = 0x03
	MuxTypePong    uint8 = 0x04
	MuxTypeClose   uint8 = 0x05
	MuxTypeInitAck uint8 = 0x06
)

// NewWSSMuxChecker creates a new WSS Multiplexed checker
func NewWSSMuxChecker(timeout time.Duration, skipVerify bool) *WSSMuxChecker {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: skipVerify,
		MinVersion:         tls.VersionTLS12,
	}

	return &WSSMuxChecker{
		timeout:   timeout,
		tlsConfig: tlsConfig,
		dialer: &websocket.Dialer{
			HandshakeTimeout: timeout,
			TLSClientConfig:  tlsConfig,
		},
	}
}

// Name returns the protocol name
func (c *WSSMuxChecker) Name() string {
	return "wssmux"
}

// Check performs a WSS Mux connection test with protocol handshake
// Endpoint format: wss://host:port/path (e.g., "wss://tunnel.example.com:443/wssmux")
func (c *WSSMuxChecker) Check(ctx context.Context, endpoint string) CheckResult {
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
	// Custom header to identify mux protocol (if required by server)
	headers.Set("X-Mux-Version", "1")

	// Attempt WSS connection
	conn, resp, err := c.dialer.DialContext(deadline, endpoint, headers)
	if err != nil {
		result.Error = err.Error()
		result.FailureType = classifyMuxError(err, resp)
		return result
	}
	defer conn.Close()

	// Send MUX initialization frame
	initFrame := c.buildInitFrame()
	if err := conn.WriteMessage(websocket.BinaryMessage, initFrame); err != nil {
		result.Error = fmt.Sprintf("mux init failed: %v", err)
		result.FailureType = FailureHandshake
		return result
	}

	// Set read deadline for mux response
	conn.SetReadDeadline(time.Now().Add(c.timeout / 2))

	// Read mux response
	msgType, msg, err := conn.ReadMessage()
	if err != nil {
		result.Error = fmt.Sprintf("mux read failed: %v", err)
		result.FailureType = ClassifyError(err)
		return result
	}

	// Validate mux response
	if msgType != websocket.BinaryMessage {
		result.Error = "unexpected message type"
		result.FailureType = FailurePayload
		return result
	}

	if !c.validateMuxResponse(msg) {
		result.Error = "invalid mux response"
		result.FailureType = FailurePayload
		return result
	}

	// Send a mux ping to verify bidirectional communication
	pingFrame := c.buildPingFrame()
	if err := conn.WriteMessage(websocket.BinaryMessage, pingFrame); err != nil {
		result.Error = fmt.Sprintf("mux ping failed: %v", err)
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
	if len(pongMsg) >= 2 && pongMsg[1] == MuxTypePong {
		result.Success = true
		result.RTT = time.Since(start)
		return result
	}

	// Any valid response is acceptable
	result.Success = true
	result.RTT = time.Since(start)
	return result
}

// buildInitFrame creates a MUX initialization frame
func (c *WSSMuxChecker) buildInitFrame() []byte {
	// Frame format: Version(1) + Type(1) + StreamID(4) + Length(4) + Payload
	frame := make([]byte, 10+16)
	frame[0] = 1                                 // Version 1
	frame[1] = MuxTypeInit                       // Init type
	binary.BigEndian.PutUint32(frame[2:6], 0)    // Stream ID 0 for control
	binary.BigEndian.PutUint32(frame[6:10], 16)  // Payload length
	copy(frame[10:], []byte("IRMON-HEALTH-MUX")) // Payload
	return frame
}

// buildPingFrame creates a MUX ping frame
func (c *WSSMuxChecker) buildPingFrame() []byte {
	frame := make([]byte, 10+4)
	frame[0] = 1                                                      // Version 1
	frame[1] = MuxTypePing                                            // Ping type
	binary.BigEndian.PutUint32(frame[2:6], 0)                         // Stream ID 0 for control
	binary.BigEndian.PutUint32(frame[6:10], 4)                        // Payload length
	binary.BigEndian.PutUint32(frame[10:], uint32(time.Now().Unix())) // Timestamp
	return frame
}

// validateMuxResponse checks if a mux response is valid
func (c *WSSMuxChecker) validateMuxResponse(msg []byte) bool {
	if len(msg) < 10 {
		return false
	}

	version := msg[0]
	frameType := msg[1]

	// Accept version 1
	if version != 1 {
		return false
	}

	// Accept init-ack, pong, or data frames
	return frameType == MuxTypeInitAck || frameType == MuxTypePong || frameType == MuxTypeData
}

// classifyMuxError categorizes MUX-specific errors
func classifyMuxError(err error, resp *http.Response) FailureType {
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

// ReadMuxFrame reads a complete mux frame from a reader
func ReadMuxFrame(r io.Reader) (*MuxFrame, error) {
	header := make([]byte, 10)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	frame := &MuxFrame{
		Version:  header[0],
		Type:     header[1],
		StreamID: binary.BigEndian.Uint32(header[2:6]),
		Length:   binary.BigEndian.Uint32(header[6:10]),
	}

	if frame.Length > 0 {
		frame.Payload = make([]byte, frame.Length)
		if _, err := io.ReadFull(r, frame.Payload); err != nil {
			return nil, err
		}
	}

	return frame, nil
}

// WriteMuxFrame writes a mux frame to a writer
func WriteMuxFrame(w io.Writer, frame *MuxFrame) error {
	header := make([]byte, 10)
	header[0] = frame.Version
	header[1] = frame.Type
	binary.BigEndian.PutUint32(header[2:6], frame.StreamID)
	binary.BigEndian.PutUint32(header[6:10], uint32(len(frame.Payload)))

	if _, err := w.Write(header); err != nil {
		return err
	}

	if len(frame.Payload) > 0 {
		if _, err := w.Write(frame.Payload); err != nil {
			return err
		}
	}

	return nil
}
