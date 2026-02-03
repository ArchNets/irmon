package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the main configuration structure
type Config struct {
	Servers     []ServerConfig   `yaml:"servers"`
	Cloudflare  CloudflareConfig `yaml:"cloudflare"`
	Scoring     ScoringConfig    `yaml:"scoring"`
	CheckConfig CheckConfig      `yaml:"check_config"`
	Logging     LoggingConfig    `yaml:"logging"`
	Metrics     MetricsConfig    `yaml:"metrics"`
	Bandwidth   BandwidthConfig  `yaml:"bandwidth"`
}

// ServerConfig represents a server to monitor
type ServerConfig struct {
	Name          string           `yaml:"name"`
	OriginIP      string           `yaml:"origin_ip"`      // Public IP for Cloudflare
	TunnelIP      string           `yaml:"tunnel_ip"`      // Overlay tunnel IP
	AgentEndpoint string           `yaml:"agent_endpoint"` // Agent HTTP endpoint for bandwidth metrics
	Protocols     []ProtocolConfig `yaml:"protocols"`
}

// ProtocolConfig represents a protocol check configuration
type ProtocolConfig struct {
	Type     string `yaml:"type"`     // wssmux, xwssmux, wss, ws, tcp, icmp
	Endpoint string `yaml:"endpoint"` // e.g., wss://tunnel.example.com:443/mux
	Timeout  int    `yaml:"timeout"`  // milliseconds
}

// CloudflareConfig represents Cloudflare API configuration
type CloudflareConfig struct {
	APIToken  string `yaml:"api_token"`
	AccountID string `yaml:"account_id"`
	ZoneID    string `yaml:"zone_id"`
	DNSName   string `yaml:"dns_name"`   // e.g. vpn.example.com
	RateLimit int    `yaml:"rate_limit"` // requests per second
	TTL       int    `yaml:"ttl"`        // DNS TTL in seconds
}

// ScoringConfig represents scoring weights and thresholds
type ScoringConfig struct {
	Weights    map[string]int  `yaml:"weights"`
	Thresholds ThresholdConfig `yaml:"thresholds"`
}

// ThresholdConfig represents state thresholds
type ThresholdConfig struct {
	FullyUsable int `yaml:"fully_usable"` // >= this score is FULLY_USABLE
	Degraded    int `yaml:"degraded"`     // >= this score is DEGRADED, below is IRAN_ONLY
}

// CheckConfig represents check timing configuration
type CheckConfig struct {
	Interval          time.Duration `yaml:"interval"`
	Timeout           time.Duration `yaml:"timeout"`
	FailureThreshold  int           `yaml:"failure_threshold"`  // Consecutive failures before state change
	RecoveryThreshold int           `yaml:"recovery_threshold"` // Consecutive successes before recovery
	FlappingWindow    time.Duration `yaml:"flapping_window"`    // Cooldown after state change
}

// LoggingConfig represents logging configuration
type LoggingConfig struct {
	Format string `yaml:"format"` // json or text
	Level  string `yaml:"level"`  // debug, info, warn, error
	Output string `yaml:"output"` // file path or stdout
}

// MetricsConfig represents Prometheus metrics configuration
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"` // e.g., :9090
	Path    string `yaml:"path"`    // e.g., /metrics
}

// BandwidthConfig represents bandwidth monitoring configuration
type BandwidthConfig struct {
	Enabled       bool          `yaml:"enabled"`        // Enable bandwidth monitoring
	Interval      time.Duration `yaml:"interval"`       // How often to collect bandwidth stats
	Interface     string        `yaml:"interface"`      // Specific interface to monitor (empty = auto-detect)
	AgentPort     int           `yaml:"agent_port"`     // Port for agent bandwidth metrics endpoint
	DatabasePath  string        `yaml:"database_path"`  // Path to SQLite database file
	RetentionDays int           `yaml:"retention_days"` // How many days to keep history (0 = forever)
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		CheckConfig: CheckConfig{
			Interval:          30 * time.Second,
			Timeout:           10 * time.Second,
			FailureThreshold:  3,
			RecoveryThreshold: 2,
			FlappingWindow:    5 * time.Minute,
		},
		Scoring: ScoringConfig{
			Weights: map[string]int{
				"wssmux":  100,
				"xwssmux": 80,
				"wss":     60,
				"ws":      40,
				"tcp":     20,
				"icmp":    5,
			},
			Thresholds: ThresholdConfig{
				FullyUsable: 60,
				Degraded:    20,
			},
		},
		Cloudflare: CloudflareConfig{
			RateLimit: 5,
			TTL:       30,
		},
		Logging: LoggingConfig{
			Format: "json",
			Level:  "info",
			Output: "stdout",
		},
		Metrics: MetricsConfig{
			Enabled: true,
			Address: ":9090",
			Path:    "/metrics",
		},
		Bandwidth: BandwidthConfig{
			Enabled:       true,
			Interval:      10 * time.Second,
			Interface:     "", // Auto-detect
			AgentPort:     9091,
			DatabasePath:  "/var/lib/irmon/bandwidth.db",
			RetentionDays: 30, // Keep 30 days of history
		},
	}
}

// Load reads configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Start with defaults
	cfg := DefaultConfig()

	// Expand environment variables
	expanded := os.ExpandEnv(string(data))

	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return cfg, nil
}

// Validate checks the configuration for errors
func (c *Config) Validate() error {
	if len(c.Servers) == 0 {
		return fmt.Errorf("at least one server must be configured")
	}

	for i, server := range c.Servers {
		if server.Name == "" {
			return fmt.Errorf("server %d: name is required", i)
		}
		if server.OriginIP == "" {
			return fmt.Errorf("server %s: origin_ip is required", server.Name)
		}
		if len(server.Protocols) == 0 {
			return fmt.Errorf("server %s: at least one protocol must be configured", server.Name)
		}

		for j, proto := range server.Protocols {
			if proto.Type == "" {
				return fmt.Errorf("server %s protocol %d: type is required", server.Name, j)
			}
			if proto.Endpoint == "" {
				return fmt.Errorf("server %s protocol %d: endpoint is required", server.Name, j)
			}
		}
	}

	if c.Cloudflare.APIToken == "" {
		return fmt.Errorf("cloudflare.api_token is required")
	}
	if c.Cloudflare.ZoneID == "" {
		return fmt.Errorf("cloudflare.zone_id is required")
	}
	if c.Cloudflare.DNSName == "" {
		return fmt.Errorf("cloudflare.dns_name is required")
	}

	return nil
}

// GetProtocolTimeout returns the timeout for a protocol, with fallback to global timeout
func (c *Config) GetProtocolTimeout(proto ProtocolConfig) time.Duration {
	if proto.Timeout > 0 {
		return time.Duration(proto.Timeout) * time.Millisecond
	}
	return c.CheckConfig.Timeout
}
