package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

var (
	listenAddrs = flag.String("listen", "0.0.0.0:8080", "Addresses to listen on (comma-separated)")
	tlsCert     = flag.String("cert", "", "TLS certificate file (enables HTTPS/WSS)")
	tlsKey      = flag.String("key", "", "TLS key file")
	enableWS    = flag.Bool("ws", true, "Enable WebSocket endpoint")
	version     = "dev"
)

// HealthResponse is the JSON response for health checks
type HealthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Uptime    string    `json:"uptime"`
	Hostname  string    `json:"hostname"`
	Version   string    `json:"version"`
	LocalAddr string    `json:"local_addr,omitempty"`
}

var (
	startTime = time.Now()
	upgrader  = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func main() {
	flag.Parse()

	// Setup logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	hostname, _ := os.Hostname()

	// Parse multiple listen addresses
	addrs := parseAddresses(*listenAddrs)
	if len(addrs) == 0 {
		slog.Error("no listen addresses specified")
		os.Exit(1)
	}

	slog.Info("starting irmon agent",
		"version", version,
		"listen", addrs,
		"hostname", hostname,
		"tls", *tlsCert != "",
	)

	// Setup HTTP handlers
	handler := setupHandlers()

	// Start all servers
	var wg sync.WaitGroup
	servers := make([]*http.Server, 0, len(addrs))

	for _, addr := range addrs {
		server := &http.Server{
			Addr:         addr,
			Handler:      loggingMiddleware(handler),
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		servers = append(servers, server)

		wg.Add(1)
		go func(srv *http.Server, address string) {
			defer wg.Done()
			var err error
			if *tlsCert != "" && *tlsKey != "" {
				slog.Info("starting HTTPS server", "addr", address)
				srv.TLSConfig = &tls.Config{
					MinVersion: tls.VersionTLS12,
				}
				err = srv.ListenAndServeTLS(*tlsCert, *tlsKey)
			} else {
				slog.Info("starting HTTP server", "addr", address)
				err = srv.ListenAndServe()
			}
			if err != nil && err != http.ErrServerClosed {
				slog.Error("server error", "addr", address, "error", err)
			}
		}(server, addr)
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan

	slog.Info("shutting down", "signal", sig)

	// Graceful shutdown all servers
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	for _, srv := range servers {
		srv.Shutdown(shutdownCtx)
	}

	wg.Wait()
	slog.Info("shutdown complete")
}

// parseAddresses splits comma-separated addresses
func parseAddresses(input string) []string {
	var addrs []string
	for _, addr := range strings.Split(input, ",") {
		addr = strings.TrimSpace(addr)
		if addr != "" {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// setupHandlers creates the HTTP mux with all endpoints
func setupHandlers() *http.ServeMux {
	mux := http.NewServeMux()

	// Health check endpoints
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/", healthHandler)

	// WebSocket endpoint
	if *enableWS {
		mux.HandleFunc("/ws", wsHandler)
		mux.HandleFunc("/wss", wsHandler)
	}

	// Status endpoint
	mux.HandleFunc("/status", statusHandler)

	return mux
}

// healthHandler responds to health check requests
func healthHandler(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	resp := HealthResponse{
		Status:    "ok",
		Timestamp: time.Now(),
		Uptime:    time.Since(startTime).Round(time.Second).String(),
		Hostname:  hostname,
		Version:   version,
		LocalAddr: r.Host, // Which address received this request
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// statusHandler returns detailed status
func statusHandler(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()

	// Get local IPs
	var ips []string
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ips = append(ips, ipnet.IP.String())
			}
		}
	}

	// Get interfaces
	interfaces := make(map[string][]string)
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				interfaces[iface.Name] = append(interfaces[iface.Name], ipnet.IP.String())
			}
		}
	}

	status := map[string]interface{}{
		"status":      "ok",
		"timestamp":   time.Now(),
		"uptime":      time.Since(startTime).Round(time.Second).String(),
		"hostname":    hostname,
		"version":     version,
		"go_version":  runtime.Version(),
		"local_ips":   ips,
		"interfaces":  interfaces,
		"goroutines":  runtime.NumGoroutine(),
		"listen_addr": r.Host,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// wsHandler handles WebSocket connections
func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	slog.Debug("websocket connection established", "remote", r.RemoteAddr)

	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	conn.SetPingHandler(func(data string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})

	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		response := append([]byte("ok:"), msg...)
		if err := conn.WriteMessage(msgType, response); err != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	}
}

// loggingMiddleware logs all requests
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"host", r.Host,
			"duration", time.Since(start),
		)
	})
}

func usage() {
	fmt.Fprintf(os.Stderr, `irmon-agent - Health check endpoint for Iranian servers

Usage:
  irmon-agent [options]

Options:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Examples:
  # Single address
  irmon-agent -listen 30.7.0.1:8080

  # Multiple addresses (comma-separated)
  irmon-agent -listen "30.7.0.1:8080,30.8.0.1:8080"

  # All interfaces
  irmon-agent -listen 0.0.0.0:8080

  # With TLS
  irmon-agent -listen "30.7.0.1:443,30.8.0.1:443" -cert cert.pem -key key.pem

Endpoints:
  GET /health  - Returns JSON health status
  GET /status  - Returns detailed status with interfaces
  WS  /ws      - WebSocket endpoint
`)
}

func init() {
	flag.Usage = usage
}
