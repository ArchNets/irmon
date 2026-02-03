package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

const (
	apiBaseURL = "https://api.cloudflare.com/client/v4"
)

// Client handles Cloudflare API interactions
type Client struct {
	apiToken    string
	accountID   string
	zoneID      string
	dnsName     string
	ttl         int
	httpClient  *http.Client
	rateLimiter *rate.Limiter
}

// Config holds client configuration
type Config struct {
	APIToken  string
	AccountID string
	ZoneID    string
	DNSName   string
	RateLimit int // requests per second
	TTL       int // DNS TTL in seconds
}

// NewClient creates a new Cloudflare API client
func NewClient(cfg Config) *Client {
	rateLimit := cfg.RateLimit
	if rateLimit <= 0 {
		rateLimit = 5
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 60 // Default to 1 minute to avoid caching bad IPs too long
	}

	return &Client{
		apiToken:    cfg.APIToken,
		accountID:   cfg.AccountID,
		zoneID:      cfg.ZoneID,
		dnsName:     cfg.DNSName,
		ttl:         ttl,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		rateLimiter: rate.NewLimiter(rate.Limit(rateLimit), 1),
	}
}

// DNSRecord represents a Cloudflare DNS record
type DNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

// APIResponse represents a Cloudflare API response
type APIResponse struct {
	Success  bool            `json:"success"`
	Errors   []APIError      `json:"errors"`
	Messages []string        `json:"messages"`
	Result   json.RawMessage `json:"result"`
}

// APIError represents a Cloudflare API error
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// GetDNSRecords retrieves all A and AAAA records for the configured DNS name
func (c *Client) GetDNSRecords(ctx context.Context) ([]DNSRecord, error) {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait: %w", err)
	}

	url := fmt.Sprintf("%s/zones/%s/dns_records?name=%s", apiBaseURL, c.zoneID, c.dnsName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %v", apiResp.Errors)
	}

	var records []DNSRecord
	if err := json.Unmarshal(apiResp.Result, &records); err != nil {
		return nil, fmt.Errorf("parsing records: %w", err)
	}

	// Filter for A and AAAA records only
	var filtered []DNSRecord
	for _, r := range records {
		if r.Type == "A" || r.Type == "AAAA" {
			filtered = append(filtered, r)
		}
	}

	return filtered, nil
}

// CreateDNSRecord creates a new DNS record
func (c *Client) CreateDNSRecord(ctx context.Context, ip string) error {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait: %w", err)
	}

	recordType := "A"
	if net.ParseIP(ip).To4() == nil {
		recordType = "AAAA"
	}

	record := DNSRecord{
		Type:    recordType,
		Name:    c.dnsName,
		Content: ip,
		TTL:     c.ttl,
		Proxied: false, // DNS Load Balancing usually uses unproxied records (DNS only)
	}

	jsonBody, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshaling record: %w", err)
	}

	url := fmt.Sprintf("%s/zones/%s/dns_records", apiBaseURL, c.zoneID)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	if !apiResp.Success {
		return fmt.Errorf("API error: %v", apiResp.Errors)
	}

	return nil
}

// DeleteDNSRecord deletes a DNS record by ID
func (c *Client) DeleteDNSRecord(ctx context.Context, id string) error {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait: %w", err)
	}

	url := fmt.Sprintf("%s/zones/%s/dns_records/%s", apiBaseURL, c.zoneID, id)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// SyncDNS ensures that the DNS records for the domain match exactly the list of desired IPs
// It adds missing IPs and removes extra IPs (only if they are A/AAAA records).
// It returns the number of additions and deletions performed.
func (c *Client) SyncDNS(ctx context.Context, desiredIPs []string) (int, int, error) {
	// 1. Get current records
	records, err := c.GetDNSRecords(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("getting current records: %w", err)
	}

	currentMap := make(map[string]DNSRecord)
	for _, r := range records {
		currentMap[r.Content] = r
	}

	desiredMap := make(map[string]bool)
	for _, ip := range desiredIPs {
		desiredMap[ip] = true
	}

	added := 0
	deleted := 0

	// 2. Add missing IPs
	for _, ip := range desiredIPs {
		if _, exists := currentMap[ip]; !exists {
			if err := c.CreateDNSRecord(ctx, ip); err != nil {
				return added, deleted, fmt.Errorf("creating record for %s: %w", ip, err)
			}
			added++
		}
	}

	// 3. Remove extra IPs
	// CAUTION: This removes ANY A/AAAA record for this name that isn't in desiredIPs.
	// We should be careful to only remove IPs that we know about (i.e. those in our config but marked unusable)
	// However, for pure LB, we typically want the DNS to reflect EXACTLY the state.
	// To be safe, let's assume if it is in the DNS but not in desired, it should be removed.
	for ip, record := range currentMap {
		if !desiredMap[ip] {
			if err := c.DeleteDNSRecord(ctx, record.ID); err != nil {
				return added, deleted, fmt.Errorf("deleting record for %s: %w", ip, err)
			}
			deleted++
		}
	}

	return added, deleted, nil
}
