package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
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

const (
	serviceName = "irmon-agent"
	binaryPath  = "/usr/local/bin/irmon-agent"
	repoURL     = "https://github.com/ArchNets/irmon"
	apiURL      = "https://api.github.com/repos/ArchNets/irmon/releases/latest"
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
	// Check for subcommands first
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "run", "start", "-listen":
			runAgent()
			return
		case "menu", "manage":
			showMenu()
			return
		case "version", "-v", "--version":
			fmt.Printf("irmon-agent %s\n", version)
			return
		case "help", "-h", "--help":
			printHelp()
			return
		}
	}

	// No args = show menu
	showMenu()
}

func printHelp() {
	fmt.Printf(`irmon-agent - Health Check Endpoint for Iranian Servers

Version: %s

Usage:
  irmon-agent                        Show interactive menu
  irmon-agent run -listen <addrs>    Run the agent
  irmon-agent menu                   Show interactive menu
  irmon-agent version                Show version

Options:
  -listen string    Addresses to listen on, comma-separated (default "0.0.0.0:8080")
  -cert string      TLS certificate file
  -key string       TLS key file
  -ws               Enable WebSocket endpoint (default true)

Examples:
  irmon-agent run -listen "30.7.0.1:8080,30.8.0.1:8080"

For more information: %s
`, version, repoURL)
}

func showMenu() {
	for {
		clearScreen()
		printHeader()
		printStatus()
		fmt.Println()
		printMenuOptions()

		choice := readChoice()
		handleMenuChoice(choice)
	}
}

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

func printHeader() {
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Printf("║      irmon-agent - Health Check Endpoint  %-15s║\n", version)
	fmt.Println("╠═══════════════════════════════════════════════════════════╣")
}

func printStatus() {
	installed := isServiceInstalled()
	running := isServiceRunning()

	statusIcon := "⚪"
	statusText := "Not installed"
	if installed {
		if running {
			statusIcon = "🟢"
			statusText = "Running"
		} else {
			statusIcon = "🔴"
			statusText = "Stopped"
		}
	}

	fmt.Printf("║  Status: %s %-47s║\n", statusIcon, statusText)

	// Show listen addresses if running
	if running {
		addrs := getListenAddresses()
		if addrs != "" {
			fmt.Printf("║  Listening: %-44s║\n", truncate(addrs, 44))
		}
	}

	// Check for updates
	latest := getLatestVersion()
	if latest != "" && latest != version && version != "dev" {
		fmt.Printf("║  ⚠️  Update available: %-35s║\n", latest)
	}

	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func printMenuOptions() {
	fmt.Println("  1) Start service")
	fmt.Println("  2) Stop service")
	fmt.Println("  3) Restart service")
	fmt.Println("  4) View logs")
	fmt.Println("  5) Configure listen addresses")
	fmt.Println("  6) Check for updates")
	fmt.Println("  7) Update irmon-agent")
	fmt.Println("  8) Install/Reinstall service")
	fmt.Println("  9) Uninstall")
	fmt.Println("  0) Exit")
	fmt.Println()
	fmt.Print("  Choose an option: ")
}

func readChoice() string {
	reader := bufio.NewReader(os.Stdin)
	choice, _ := reader.ReadString('\n')
	return strings.TrimSpace(choice)
}

func handleMenuChoice(choice string) {
	switch choice {
	case "1":
		startService()
	case "2":
		stopService()
	case "3":
		restartService()
	case "4":
		viewLogs()
	case "5":
		configureAddresses()
	case "6":
		checkUpdates()
	case "7":
		updateBinary()
	case "8":
		installService()
	case "9":
		uninstall()
	case "0", "q", "exit":
		fmt.Println("Goodbye!")
		os.Exit(0)
	default:
		fmt.Println("Invalid option")
		time.Sleep(1 * time.Second)
	}
}

func isServiceInstalled() bool {
	_, err := os.Stat("/etc/systemd/system/irmon-agent.service")
	return err == nil
}

func isServiceRunning() bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", serviceName)
	return cmd.Run() == nil
}

func getListenAddresses() string {
	// Read from systemd service
	cmd := exec.Command("systemctl", "show", serviceName, "-p", "ExecStart", "--value")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// Parse -listen from ExecStart
	parts := strings.Fields(string(out))
	for i, p := range parts {
		if p == "-listen" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func startService() {
	fmt.Println("\nStarting irmon-agent service...")
	runSystemctl("start", serviceName)
	pressEnterToContinue()
}

func stopService() {
	fmt.Println("\nStopping irmon-agent service...")
	runSystemctl("stop", serviceName)
	pressEnterToContinue()
}

func restartService() {
	fmt.Println("\nRestarting irmon-agent service...")
	runSystemctl("restart", serviceName)
	pressEnterToContinue()
}

func viewLogs() {
	fmt.Println("\nShowing logs (press Ctrl+C to exit)...")
	fmt.Println("─────────────────────────────────────────")
	cmd := exec.Command("journalctl", "-u", serviceName, "-f", "--no-pager", "-n", "50")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

func configureAddresses() {
	fmt.Println("\nConfigure Listen Addresses")
	fmt.Println("─────────────────────────────────────────")

	// Show current interfaces
	fmt.Println("\nAvailable network interfaces:")
	showNetworkInterfaces()

	fmt.Println("\nEnter IP:Port pairs to listen on (comma-separated)")
	fmt.Println("Example: 30.7.0.1:8080,30.8.0.1:8080")
	fmt.Print("\nListen addresses: ")

	reader := bufio.NewReader(os.Stdin)
	addrs, _ := reader.ReadString('\n')
	addrs = strings.TrimSpace(addrs)

	if addrs == "" {
		fmt.Println("Cancelled")
		pressEnterToContinue()
		return
	}

	// Validate addresses
	for _, addr := range strings.Split(addrs, ",") {
		addr = strings.TrimSpace(addr)
		if !strings.Contains(addr, ":") {
			fmt.Printf("Invalid address (missing port): %s\n", addr)
			pressEnterToContinue()
			return
		}
	}

	// Update service file
	updateServiceFile(addrs)

	fmt.Println("\n✓ Configuration updated!")
	fmt.Println("Restart the service to apply changes.")
	pressEnterToContinue()
}

func showNetworkInterfaces() {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				fmt.Printf("  %s: %s\n", iface.Name, ipnet.IP.String())
			}
		}
	}
}

func getLatestVersion() string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return ""
	}
	return release.TagName
}

func checkUpdates() {
	fmt.Println("\nChecking for updates...")
	latest := getLatestVersion()
	if latest == "" {
		fmt.Println("Failed to check for updates")
	} else if latest == version {
		fmt.Printf("You are running the latest version: %s\n", version)
	} else {
		fmt.Printf("Current version: %s\n", version)
		fmt.Printf("Latest version:  %s\n", latest)
		fmt.Println("\nRun 'Update irmon-agent' to update.")
	}
	pressEnterToContinue()
}

func updateBinary() {
	fmt.Println("\nUpdating irmon-agent...")

	latest := getLatestVersion()
	if latest == "" {
		fmt.Println("Failed to get latest version")
		pressEnterToContinue()
		return
	}

	if latest == version {
		fmt.Printf("Already running latest version: %s\n", version)
		pressEnterToContinue()
		return
	}

	fmt.Printf("Updating from %s to %s...\n", version, latest)

	arch := runtime.GOARCH
	downloadURL := fmt.Sprintf("%s/releases/download/%s/irmon-agent-linux-%s", repoURL, latest, arch)
	fmt.Printf("Downloading from %s...\n", downloadURL)

	resp, err := http.Get(downloadURL)
	if err != nil {
		fmt.Printf("Download failed: %v\n", err)
		pressEnterToContinue()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Printf("Download failed: HTTP %d\n", resp.StatusCode)
		pressEnterToContinue()
		return
	}

	tmpFile := "/tmp/irmon-agent-update"
	out, err := os.Create(tmpFile)
	if err != nil {
		fmt.Printf("Failed to create temp file: %v\n", err)
		pressEnterToContinue()
		return
	}

	_, err = io.Copy(out, resp.Body)
	out.Close()
	if err != nil {
		fmt.Printf("Failed to save file: %v\n", err)
		pressEnterToContinue()
		return
	}

	os.Chmod(tmpFile, 0755)

	wasRunning := isServiceRunning()
	if wasRunning {
		fmt.Println("Stopping service...")
		runSystemctl("stop", serviceName)
	}

	fmt.Println("Installing new binary...")
	if err := os.Rename(tmpFile, binaryPath); err != nil {
		cmd := exec.Command("sudo", "mv", tmpFile, binaryPath)
		if err := cmd.Run(); err != nil {
			fmt.Printf("Failed to install: %v\n", err)
			pressEnterToContinue()
			return
		}
	}

	if wasRunning {
		fmt.Println("Starting service...")
		runSystemctl("start", serviceName)
	}

	fmt.Printf("\n✓ Updated to %s\n", latest)
	fmt.Println("Please restart the menu to see the new version.")
	pressEnterToContinue()
}

func installService() {
	fmt.Println("\nInstalling irmon-agent service...")

	// Get listen addresses
	fmt.Println("\nAvailable network interfaces:")
	showNetworkInterfaces()

	fmt.Println("\nEnter IP:Port pairs to listen on (comma-separated)")
	fmt.Println("Example: 30.7.0.1:8080,30.8.0.1:8080")
	fmt.Print("\nListen addresses [0.0.0.0:8080]: ")

	reader := bufio.NewReader(os.Stdin)
	addrs, _ := reader.ReadString('\n')
	addrs = strings.TrimSpace(addrs)
	if addrs == "" {
		addrs = "0.0.0.0:8080"
	}

	createServiceFile(addrs)

	runSystemctl("daemon-reload", "")
	runSystemctl("enable", serviceName)

	fmt.Println("\n✓ Service installed!")
	fmt.Printf("  Listening on: %s\n", addrs)
	fmt.Println("\nStart the service from the menu.")
	pressEnterToContinue()
}

func uninstall() {
	fmt.Println("\n⚠️  This will remove irmon-agent from the system.")
	fmt.Print("Are you sure? (yes/no): ")

	reader := bufio.NewReader(os.Stdin)
	confirm, _ := reader.ReadString('\n')
	confirm = strings.TrimSpace(strings.ToLower(confirm))

	if confirm != "yes" {
		fmt.Println("Cancelled")
		pressEnterToContinue()
		return
	}

	fmt.Println("\nUninstalling...")

	runSystemctl("stop", serviceName)
	runSystemctl("disable", serviceName)
	os.Remove("/etc/systemd/system/irmon-agent.service")
	runSystemctl("daemon-reload", "")

	fmt.Println("\n✓ Service uninstalled")
	fmt.Printf("Binary kept at: %s\n", binaryPath)
	pressEnterToContinue()
}

func runSystemctl(action, service string) {
	var cmd *exec.Cmd
	if service == "" {
		cmd = exec.Command("sudo", "systemctl", action)
	} else {
		cmd = exec.Command("sudo", "systemctl", action, service)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

func pressEnterToContinue() {
	fmt.Print("\nPress Enter to continue...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func createServiceFile(listenAddrs string) {
	serviceContent := fmt.Sprintf(`[Unit]
Description=irmon Agent - Health Check Endpoint
Documentation=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=%s run -listen %s
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=irmon-agent

[Install]
WantedBy=multi-user.target
`, repoURL, binaryPath, listenAddrs)

	os.WriteFile("/etc/systemd/system/irmon-agent.service", []byte(serviceContent), 0644)
}

func updateServiceFile(listenAddrs string) {
	createServiceFile(listenAddrs)
	runSystemctl("daemon-reload", "")
}

// =============================================================================
// AGENT MODE (original functionality)
// =============================================================================

func runAgent() {
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

	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/", healthHandler)

	if *enableWS {
		mux.HandleFunc("/ws", wsHandler)
		mux.HandleFunc("/wss", wsHandler)
	}

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
		LocalAddr: r.Host,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// statusHandler returns detailed status
func statusHandler(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()

	var ips []string
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ips = append(ips, ipnet.IP.String())
			}
		}
	}

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
