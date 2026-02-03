package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/archnet/irmon/internal/checker"
	"github.com/archnet/irmon/internal/cloudflare"
	"github.com/archnet/irmon/internal/config"
	"github.com/archnet/irmon/internal/scoring"
	"github.com/archnet/irmon/internal/state"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	configPath = flag.String("config", "/etc/irmon/config.yaml", "Path to configuration file")
	version    = "dev"
)

const (
	serviceName = "irmon"
	configDir   = "/etc/irmon"
	configFile  = "/etc/irmon/config.yaml"
	envFile     = "/etc/irmon/env"
	binaryPath  = "/usr/local/bin/irmon"
	repoURL     = "https://github.com/ArchNets/irmon"
	apiURL      = "https://api.github.com/repos/ArchNets/irmon/releases/latest"
)

// Metrics for Prometheus
var (
	serverScore = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "irmon_server_score",
			Help: "Current health score for each server",
		},
		[]string{"server"},
	)
	serverState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "irmon_server_state",
			Help: "Current state for each server (0=IRAN_ONLY, 1=DEGRADED, 2=FULLY_USABLE)",
		},
		[]string{"server"},
	)
	serverWeight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "irmon_server_weight",
			Help: "Current Cloudflare weight for each server",
		},
		[]string{"server"},
	)
	protocolCheck = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "irmon_protocol_check",
			Help: "Protocol check result (0=fail, 1=success)",
		},
		[]string{"server", "protocol"},
	)
	protocolRTT = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "irmon_protocol_rtt_ms",
			Help: "Protocol check RTT in milliseconds",
		},
		[]string{"server", "protocol"},
	)
	checkCycleTime = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "irmon_check_cycle_seconds",
			Help:    "Time taken for a complete check cycle",
			Buckets: prometheus.DefBuckets,
		},
	)
)

func init() {
	prometheus.MustRegister(serverScore)
	prometheus.MustRegister(serverState)
	prometheus.MustRegister(serverWeight)
	prometheus.MustRegister(protocolCheck)
	prometheus.MustRegister(protocolRTT)
	prometheus.MustRegister(checkCycleTime)
}

func main() {
	// Check for subcommands first
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "run", "start", "-config":
			// Continue to normal execution
			runDaemon()
			return
		case "menu", "manage":
			showMenu()
			return
		case "version", "-v", "--version":
			fmt.Printf("irmon %s\n", version)
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
	fmt.Printf(`irmon - Iran-Aware Health Monitoring System

Version: %s

Usage:
  irmon              Show interactive menu
  irmon run          Run the monitoring daemon
  irmon menu         Show interactive menu
  irmon version      Show version

Options:
  -config string     Path to configuration file (default "/etc/irmon/config.yaml")

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
	fmt.Printf("║        irmon - Health Monitoring System  %-16s║\n", version)
	fmt.Println("╠═══════════════════════════════════════════════════════════╣")
}

func printStatus() {
	// Check if service is installed
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

	// Check for updates
	latest := getLatestVersion()
	if latest != "" && latest != version && version != "dev" {
		fmt.Printf("║  ⚠️  Update available: %-35s║\n", latest)
	}

	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
}

func printMenuOptions() {
	fmt.Println("  1) Start service")
	fmt.Println("  2) Stop service")
	fmt.Println("  3) Restart service")
	fmt.Println("  4) View logs")
	fmt.Println("  5) Edit config")
	fmt.Println("  6) Edit credentials")
	fmt.Println("  7) Check for updates")
	fmt.Println("  8) Update irmon")
	fmt.Println("  9) Install/Reinstall service")
	fmt.Println("  10) Uninstall")
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
		editConfig()
	case "6":
		editCredentials()
	case "7":
		checkUpdates()
	case "8":
		updateBinary()
	case "9":
		installService()
	case "10":
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
	_, err := os.Stat("/etc/systemd/system/irmon.service")
	return err == nil
}

func isServiceRunning() bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", serviceName)
	return cmd.Run() == nil
}

func startService() {
	fmt.Println("\nStarting irmon service...")
	runSystemctl("start", serviceName)
	pressEnterToContinue()
}

func stopService() {
	fmt.Println("\nStopping irmon service...")
	runSystemctl("stop", serviceName)
	pressEnterToContinue()
}

func restartService() {
	fmt.Println("\nRestarting irmon service...")
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

// getEditor returns the preferred editor (vim > nano) or EDITOR env var
func getEditor() string {
	if editor := os.Getenv("EDITOR"); editor != "" {
		return editor
	}
	// Check if vim is available
	if _, err := exec.LookPath("vim"); err == nil {
		return "vim"
	}
	// Fallback to nano
	return "nano"
}

func editConfig() {
	editor := getEditor()
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		fmt.Printf("\nConfig file not found: %s\n", configFile)
		fmt.Println("Run 'Install service' first to create the config file.")
		pressEnterToContinue()
		return
	}
	cmd := exec.Command(editor, configFile)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

func editCredentials() {
	editor := getEditor()
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		fmt.Printf("\nCredentials file not found: %s\n", envFile)
		fmt.Println("Run 'Install service' first to create the credentials file.")
		pressEnterToContinue()
		return
	}
	cmd := exec.Command(editor, envFile)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
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
		fmt.Println("\nRun 'Update irmon' to update.")
	}
	pressEnterToContinue()
}

func updateBinary() {
	fmt.Println("\nUpdating irmon...")

	// Get latest version
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

	// Detect architecture
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "amd64"
	} else if arch == "arm64" {
		arch = "arm64"
	} else {
		fmt.Printf("Unsupported architecture: %s\n", arch)
		pressEnterToContinue()
		return
	}

	// Download new binary
	downloadURL := fmt.Sprintf("%s/releases/download/%s/irmon-linux-%s", repoURL, latest, arch)
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

	// Save to temp file
	tmpFile := "/tmp/irmon-update"
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

	// Make executable
	os.Chmod(tmpFile, 0755)

	// Replace binary
	wasRunning := isServiceRunning()
	if wasRunning {
		fmt.Println("Stopping service...")
		runSystemctl("stop", serviceName)
	}

	fmt.Println("Installing new binary...")
	if err := os.Rename(tmpFile, binaryPath); err != nil {
		// Try with sudo
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
	fmt.Println("Restarting menu with new version...")
	time.Sleep(1 * time.Second)

	// Re-exec the NEW binary (must use binaryPath, not os.Executable which is the old one)
	err = syscall.Exec(binaryPath, []string{binaryPath}, os.Environ())
	if err != nil {
		fmt.Printf("Failed to restart: %v\n", err)
		fmt.Println("Please run 'irmon' again to see the new version.")
		pressEnterToContinue()
	}
}

func installService() {
	fmt.Println("\nInstalling irmon service...")

	// Create config directory
	os.MkdirAll(configDir, 0755)

	// Create config file if not exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		fmt.Println("Creating config file...")
		createDefaultConfig()
	}

	// Create env file if not exists
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		fmt.Println("Creating credentials file...")
		createDefaultEnv()
	}

	// Create systemd service
	fmt.Println("Creating systemd service...")
	createServiceFile()

	// Reload systemd
	runSystemctl("daemon-reload", "")
	runSystemctl("enable", serviceName)

	fmt.Println("\n✓ Service installed!")
	fmt.Println("\nNext steps:")
	fmt.Printf("  1. Edit config: %s\n", configFile)
	fmt.Printf("  2. Add Cloudflare credentials: %s\n", envFile)
	fmt.Println("  3. Start service from the menu")
	pressEnterToContinue()
}

func uninstall() {
	fmt.Println("\n⚠️  This will remove irmon from the system.")
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

	// Stop and disable service
	runSystemctl("stop", serviceName)
	runSystemctl("disable", serviceName)

	// Remove files
	os.Remove("/etc/systemd/system/irmon.service")
	runSystemctl("daemon-reload", "")

	fmt.Println("\n✓ Service uninstalled")
	fmt.Printf("Config files kept at: %s\n", configDir)
	fmt.Printf("Binary kept at: %s\n", binaryPath)
	fmt.Println("\nTo completely remove, also run:")
	fmt.Printf("  sudo rm -rf %s %s\n", configDir, binaryPath)
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

func createDefaultConfig() {
	configContent := `# irmon configuration
check_config:
  interval: 30s
  timeout: 10s
  failure_threshold: 3
  recovery_threshold: 2
  flapping_window: 5m

scoring:
  weights:
    tcp: 100
    icmp: 20
  thresholds:
    fully_usable: 80
    degraded: 20

cloudflare:
  api_token: "${CF_API_TOKEN}"
  account_id: "${CF_ACCOUNT_ID}"
  pool_id: "${CF_POOL_ID}"
  rate_limit: 5
  ttl: 30

servers:
  # Add your Iranian servers here
  # - name: "iran-1"
  #   origin_ip: "185.x.x.x"
  #   tunnel_ip: "30.1.0.1"
  #   protocols:
  #     - type: tcp
  #       endpoint: "30.1.0.1:8080"
  #       timeout: 5000

logging:
  format: json
  level: info

metrics:
  enabled: true
  address: ":9090"
  path: /metrics
`
	os.WriteFile(configFile, []byte(configContent), 0644)
}

func createDefaultEnv() {
	envContent := `# Cloudflare credentials
CF_API_TOKEN=your-api-token-here
CF_ACCOUNT_ID=your-account-id-here
CF_POOL_ID=your-pool-id-here
`
	os.WriteFile(envFile, []byte(envContent), 0600)
}

func createServiceFile() {
	serviceContent := fmt.Sprintf(`[Unit]
Description=irmon - Iran-Aware Health Monitoring System
Documentation=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=%s run -config %s
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=irmon
EnvironmentFile=%s

[Install]
WantedBy=multi-user.target
`, repoURL, binaryPath, configFile, envFile)

	os.WriteFile("/etc/systemd/system/irmon.service", []byte(serviceContent), 0644)
}

// =============================================================================
// DAEMON MODE (original functionality)
// =============================================================================

func runDaemon() {
	flag.Parse()

	// Setup logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("starting irmon", "version", version, "config", *configPath)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize components
	registry := setupCheckers(cfg)
	scorer := scoring.NewScorer(
		cfg.Scoring.Weights,
		cfg.Scoring.Thresholds.FullyUsable,
		cfg.Scoring.Thresholds.Degraded,
	)
	stateManager := state.NewManager(
		cfg.CheckConfig.FailureThreshold,
		cfg.CheckConfig.RecoveryThreshold,
		cfg.CheckConfig.FlappingWindow,
		cfg.Scoring.Thresholds.Degraded,
	)

	// Register all servers
	for _, server := range cfg.Servers {
		stateManager.RegisterServer(server.Name, server.OriginIP)
	}

	// Initialize Cloudflare client
	cfClient := cloudflare.NewClient(cloudflare.Config{
		APIToken:  cfg.Cloudflare.APIToken,
		AccountID: cfg.Cloudflare.AccountID,
		PoolID:    cfg.Cloudflare.PoolID,
		RateLimit: cfg.Cloudflare.RateLimit,
	})

	// Refresh Cloudflare cache on startup
	if err := cfClient.RefreshCache(ctx); err != nil {
		slog.Warn("failed to refresh Cloudflare cache", "error", err)
	}

	// Start metrics server if enabled
	if cfg.Metrics.Enabled {
		go startMetricsServer(cfg.Metrics.Address, cfg.Metrics.Path)
	}

	// Start health check loop
	go runCheckLoop(ctx, cfg, registry, scorer, stateManager, cfClient)

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan

	slog.Info("shutting down", "signal", sig)
	cancel()

	// Give goroutines time to clean up
	time.Sleep(2 * time.Second)
	slog.Info("shutdown complete")
}

// setupCheckers initializes all protocol checkers
func setupCheckers(cfg *config.Config) *checker.Registry {
	registry := checker.NewRegistry()

	// Default timeout
	defaultTimeout := cfg.CheckConfig.Timeout

	// Register all checker types
	registry.Register(checker.NewICMPChecker(defaultTimeout))
	registry.Register(checker.NewTCPChecker(defaultTimeout))
	registry.Register(checker.NewWSChecker(defaultTimeout))
	registry.Register(checker.NewWSSChecker(defaultTimeout, true))
	registry.Register(checker.NewWSSMuxChecker(defaultTimeout, true))
	registry.Register(checker.NewXWSSMuxChecker(defaultTimeout, true))

	return registry
}

// runCheckLoop continuously checks all servers
func runCheckLoop(
	ctx context.Context,
	cfg *config.Config,
	registry *checker.Registry,
	scorer *scoring.Scorer,
	stateManager *state.Manager,
	cfClient *cloudflare.Client,
) {
	ticker := time.NewTicker(cfg.CheckConfig.Interval)
	defer ticker.Stop()

	// Run first check immediately
	runCheckCycle(ctx, cfg, registry, scorer, stateManager, cfClient)

	for {
		select {
		case <-ctx.Done():
			slog.Info("check loop stopped")
			return
		case <-ticker.C:
			runCheckCycle(ctx, cfg, registry, scorer, stateManager, cfClient)
		}
	}
}

// runCheckCycle performs a complete check cycle for all servers
func runCheckCycle(
	ctx context.Context,
	cfg *config.Config,
	registry *checker.Registry,
	scorer *scoring.Scorer,
	stateManager *state.Manager,
	cfClient *cloudflare.Client,
) {
	cycleStart := time.Now()
	slog.Debug("starting check cycle")

	var wg sync.WaitGroup
	weightUpdates := make(map[string]int)
	var mu sync.Mutex

	for _, server := range cfg.Servers {
		wg.Add(1)
		go func(srv config.ServerConfig) {
			defer wg.Done()

			// Run checks for this server
			results := checkServer(ctx, cfg, registry, srv)

			// Calculate score
			scoreResult := scorer.Calculate(srv.Name, results)
			weight := scorer.CalculateCloudflareWeight(scoreResult)

			// Update state
			stateChange := stateManager.Update(scoreResult, weight)

			// Log results
			logCheckResult(srv.Name, scoreResult, weight, stateChange)

			// Update metrics
			updateMetrics(srv.Name, scoreResult, weight)

			// Collect weight updates
			mu.Lock()
			weightUpdates[srv.OriginIP] = weight
			mu.Unlock()

		}(server)
	}

	wg.Wait()

	// Batch update Cloudflare
	if len(weightUpdates) > 0 {
		if err := cfClient.BatchUpdateOrigins(ctx, weightUpdates); err != nil {
			slog.Error("failed to update Cloudflare", "error", err)
		} else {
			slog.Debug("updated Cloudflare weights", "count", len(weightUpdates))
		}
	}

	cycleDuration := time.Since(cycleStart)
	checkCycleTime.Observe(cycleDuration.Seconds())
	slog.Debug("check cycle complete", "duration", cycleDuration)
}

// checkServer runs all configured protocol checks for a server
func checkServer(
	ctx context.Context,
	cfg *config.Config,
	registry *checker.Registry,
	server config.ServerConfig,
) []checker.CheckResult {
	var results []checker.CheckResult

	for _, proto := range server.Protocols {
		c, ok := registry.Get(proto.Type)
		if !ok {
			slog.Warn("unknown protocol type", "server", server.Name, "protocol", proto.Type)
			continue
		}

		// Create timeout context for this check
		timeout := cfg.GetProtocolTimeout(proto)
		checkCtx, cancel := context.WithTimeout(ctx, timeout)

		result := c.Check(checkCtx, proto.Endpoint)
		cancel()

		results = append(results, result)

		// Log individual check
		if result.Success {
			slog.Debug("protocol check passed",
				"server", server.Name,
				"protocol", result.Protocol,
				"rtt_ms", result.RTT.Milliseconds(),
			)
		} else {
			slog.Debug("protocol check failed",
				"server", server.Name,
				"protocol", result.Protocol,
				"error", result.Error,
				"failure_type", result.FailureType,
			)
		}
	}

	return results
}

// logCheckResult logs the scoring result for a server
func logCheckResult(
	serverName string,
	result scoring.ScoreResult,
	weight int,
	stateChange *state.StateChange,
) {
	logger := slog.With(
		"server", serverName,
		"score", result.TotalScore,
		"state", result.State,
		"weight", weight,
		"working_critical", result.WorkingCritical,
	)

	if stateChange != nil {
		logger.Info("server state changed",
			"old_state", stateChange.OldState,
			"new_state", stateChange.NewState,
			"reason", stateChange.Reason,
		)
	} else {
		logger.Debug("server check complete")
	}
}

// updateMetrics updates Prometheus metrics
func updateMetrics(serverName string, result scoring.ScoreResult, weight int) {
	serverScore.WithLabelValues(serverName).Set(float64(result.TotalScore))
	serverWeight.WithLabelValues(serverName).Set(float64(weight))

	// Map state to numeric value
	stateValue := 0.0
	switch result.State {
	case scoring.StateDegraded:
		stateValue = 1.0
	case scoring.StateFullyUsable:
		stateValue = 2.0
	}
	serverState.WithLabelValues(serverName).Set(stateValue)

	// Update per-protocol metrics
	for proto, ps := range result.ProtocolScores {
		checkValue := 0.0
		if ps.Success {
			checkValue = 1.0
		}
		protocolCheck.WithLabelValues(serverName, proto).Set(checkValue)
		protocolRTT.WithLabelValues(serverName, proto).Set(float64(ps.RTT))
	}
}

// startMetricsServer starts the Prometheus metrics HTTP server
func startMetricsServer(address, path string) {
	mux := http.NewServeMux()
	mux.Handle(path, promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/status", statusHandler)

	slog.Info("starting metrics server", "address", address, "path", path)
	if err := http.ListenAndServe(address, mux); err != nil {
		slog.Error("metrics server error", "error", err)
	}
}

// statusHandler returns current server states as JSON
func statusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "running"})
}
