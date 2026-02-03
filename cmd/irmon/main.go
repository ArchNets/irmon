package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	version    = "dev" // Set by build
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
	registry.Register(checker.NewWSSChecker(defaultTimeout, true)) // Skip TLS verify for self-signed certs
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
	// This would need access to stateManager - simplified for now
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "running"})
}
