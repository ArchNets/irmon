package bandwidth

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// InterfaceStats represents network statistics for a single interface
type InterfaceStats struct {
	Name      string    `json:"name"`
	RxBytes   uint64    `json:"rx_bytes"`
	TxBytes   uint64    `json:"tx_bytes"`
	RxPackets uint64    `json:"rx_packets"`
	TxPackets uint64    `json:"tx_packets"`
	Timestamp time.Time `json:"timestamp"`

	// Calculated rates (bytes per second)
	RxBytesPerSec uint64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec uint64 `json:"tx_bytes_per_sec"`
}

// Collector collects bandwidth statistics from /proc/net/dev
type Collector struct {
	mu            sync.RWMutex
	previousStats map[string]InterfaceStats
	targetIface   string // Optional: specific interface to monitor
}

// NewCollector creates a new bandwidth collector
// targetIface can be empty to auto-detect, or specify like "wg0", "eth0", etc.
func NewCollector(targetIface string) *Collector {
	return &Collector{
		previousStats: make(map[string]InterfaceStats),
		targetIface:   targetIface,
	}
}

// Collect reads current network statistics from /proc/net/dev
func (c *Collector) Collect() (map[string]InterfaceStats, error) {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, fmt.Errorf("opening /proc/net/dev: %w", err)
	}
	defer file.Close()

	currentStats := make(map[string]InterfaceStats)
	now := time.Now()

	scanner := bufio.NewScanner(file)
	// Skip header lines
	scanner.Scan()
	scanner.Scan()

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// Interface name has colon suffix
		ifaceName := strings.TrimSuffix(fields[0], ":")

		// Skip loopback
		if ifaceName == "lo" {
			continue
		}

		// If target interface is specified, only collect that one
		if c.targetIface != "" && ifaceName != c.targetIface {
			continue
		}

		rxBytes, _ := strconv.ParseUint(fields[1], 10, 64)
		rxPackets, _ := strconv.ParseUint(fields[2], 10, 64)
		txBytes, _ := strconv.ParseUint(fields[9], 10, 64)
		txPackets, _ := strconv.ParseUint(fields[10], 10, 64)

		stats := InterfaceStats{
			Name:      ifaceName,
			RxBytes:   rxBytes,
			TxBytes:   txBytes,
			RxPackets: rxPackets,
			TxPackets: txPackets,
			Timestamp: now,
		}

		// Calculate rates if we have previous data
		c.mu.RLock()
		if prev, exists := c.previousStats[ifaceName]; exists {
			duration := now.Sub(prev.Timestamp).Seconds()
			if duration > 0 {
				stats.RxBytesPerSec = uint64(float64(rxBytes-prev.RxBytes) / duration)
				stats.TxBytesPerSec = uint64(float64(txBytes-prev.TxBytes) / duration)
			}
		}
		c.mu.RUnlock()

		currentStats[ifaceName] = stats
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading /proc/net/dev: %w", err)
	}

	// Store for next calculation
	c.mu.Lock()
	c.previousStats = currentStats
	c.mu.Unlock()

	return currentStats, nil
}

// GetPrimaryInterface attempts to determine the primary network interface
// by finding the one with the most traffic
func (c *Collector) GetPrimaryInterface() (string, error) {
	stats, err := c.Collect()
	if err != nil {
		return "", err
	}

	var primaryIface string
	var maxTraffic uint64

	for iface, stat := range stats {
		totalTraffic := stat.RxBytes + stat.TxBytes
		if totalTraffic > maxTraffic {
			maxTraffic = totalTraffic
			primaryIface = iface
		}
	}

	if primaryIface == "" {
		return "", fmt.Errorf("no network interfaces found")
	}

	return primaryIface, nil
}

// CollectInterface collects stats for a specific interface or primary if not specified
func (c *Collector) CollectInterface(ifaceName string) (*InterfaceStats, error) {
	if ifaceName == "" {
		var err error
		ifaceName, err = c.GetPrimaryInterface()
		if err != nil {
			return nil, err
		}
	}

	stats, err := c.Collect()
	if err != nil {
		return nil, err
	}

	stat, exists := stats[ifaceName]
	if !exists {
		return nil, fmt.Errorf("interface %s not found", ifaceName)
	}

	return &stat, nil
}
