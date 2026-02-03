package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
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
	poolID      string
	httpClient  *http.Client
	rateLimiter *rate.Limiter
	mu          sync.Mutex

	// Cache to avoid unnecessary updates
	originWeights map[string]int
	lastUpdate    map[string]time.Time
}

// Config holds client configuration
type Config struct {
	APIToken  string
	AccountID string
	PoolID    string
	RateLimit int // requests per second
}

// NewClient creates a new Cloudflare API client
func NewClient(cfg Config) *Client {
	rateLimit := cfg.RateLimit
	if rateLimit <= 0 {
		rateLimit = 5
	}

	return &Client{
		apiToken:      cfg.APIToken,
		accountID:     cfg.AccountID,
		poolID:        cfg.PoolID,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		rateLimiter:   rate.NewLimiter(rate.Limit(rateLimit), 1),
		originWeights: make(map[string]int),
		lastUpdate:    make(map[string]time.Time),
	}
}

// Origin represents a Cloudflare load balancer origin
type Origin struct {
	Name    string              `json:"name"`
	Address string              `json:"address"`
	Enabled bool                `json:"enabled"`
	Weight  float64             `json:"weight"`
	Header  map[string][]string `json:"header,omitempty"`
}

// Pool represents a Cloudflare load balancer pool
type Pool struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	Origins     []Origin `json:"origins"`
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

// GetPool retrieves the current pool configuration
func (c *Client) GetPool(ctx context.Context) (*Pool, error) {
	// Wait for rate limiter
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait: %w", err)
	}

	url := fmt.Sprintf("%s/accounts/%s/load_balancers/pools/%s", apiBaseURL, c.accountID, c.poolID)

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

	var pool Pool
	if err := json.Unmarshal(apiResp.Result, &pool); err != nil {
		return nil, fmt.Errorf("parsing pool: %w", err)
	}

	return &pool, nil
}

// UpdateOriginWeight updates the weight for a specific origin IP
// This is idempotent - it won't make API calls if the weight hasn't changed
func (c *Client) UpdateOriginWeight(ctx context.Context, originIP string, weight int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if weight has changed
	if currentWeight, ok := c.originWeights[originIP]; ok {
		if currentWeight == weight {
			return nil // No change needed
		}
	}

	// Get current pool configuration
	pool, err := c.GetPool(ctx)
	if err != nil {
		return fmt.Errorf("getting pool: %w", err)
	}

	// Find and update the origin
	found := false
	for i, origin := range pool.Origins {
		if origin.Address == originIP {
			found = true
			// Calculate normalized weight (Cloudflare uses 0.0-1.0)
			normalizedWeight := float64(weight) / 100.0
			if normalizedWeight < 0 {
				normalizedWeight = 0
			}
			if normalizedWeight > 1 {
				normalizedWeight = 1
			}

			pool.Origins[i].Weight = normalizedWeight
			// Disable if weight is 0, otherwise enable
			pool.Origins[i].Enabled = weight > 0
			break
		}
	}

	if !found {
		return fmt.Errorf("origin %s not found in pool", originIP)
	}

	// Update the pool
	if err := c.updatePool(ctx, pool); err != nil {
		return err
	}

	// Update cache
	c.originWeights[originIP] = weight
	c.lastUpdate[originIP] = time.Now()

	return nil
}

// updatePool sends the updated pool configuration to Cloudflare
func (c *Client) updatePool(ctx context.Context, pool *Pool) error {
	// Wait for rate limiter
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait: %w", err)
	}

	url := fmt.Sprintf("%s/accounts/%s/load_balancers/pools/%s", apiBaseURL, c.accountID, c.poolID)

	// Only send origins field to update
	updateBody := struct {
		Origins []Origin `json:"origins"`
	}{
		Origins: pool.Origins,
	}

	jsonBody, err := json.Marshal(updateBody)
	if err != nil {
		return fmt.Errorf("marshaling body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(jsonBody))
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

// DisableOrigin sets an origin's weight to 0 and disables it
// IMPORTANT: This does NOT delete the origin
func (c *Client) DisableOrigin(ctx context.Context, originIP string) error {
	return c.UpdateOriginWeight(ctx, originIP, 0)
}

// EnableOrigin re-enables an origin with the specified weight
func (c *Client) EnableOrigin(ctx context.Context, originIP string, weight int) error {
	if weight <= 0 {
		weight = 50 // Default to 50% if no weight specified
	}
	return c.UpdateOriginWeight(ctx, originIP, weight)
}

// BatchUpdateOrigins updates multiple origins in a single API call
func (c *Client) BatchUpdateOrigins(ctx context.Context, weights map[string]int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if any weight has changed
	hasChanges := false
	for ip, weight := range weights {
		if currentWeight, ok := c.originWeights[ip]; !ok || currentWeight != weight {
			hasChanges = true
			break
		}
	}

	if !hasChanges {
		return nil // No changes needed
	}

	// Get current pool configuration
	pool, err := c.GetPool(ctx)
	if err != nil {
		return fmt.Errorf("getting pool: %w", err)
	}

	// Update all matching origins
	for i, origin := range pool.Origins {
		if weight, ok := weights[origin.Address]; ok {
			normalizedWeight := float64(weight) / 100.0
			if normalizedWeight < 0 {
				normalizedWeight = 0
			}
			if normalizedWeight > 1 {
				normalizedWeight = 1
			}

			pool.Origins[i].Weight = normalizedWeight
			pool.Origins[i].Enabled = weight > 0
		}
	}

	// Update the pool
	if err := c.updatePool(ctx, pool); err != nil {
		return err
	}

	// Update cache
	now := time.Now()
	for ip, weight := range weights {
		c.originWeights[ip] = weight
		c.lastUpdate[ip] = now
	}

	return nil
}

// GetCachedWeight returns the last known weight for an origin
func (c *Client) GetCachedWeight(originIP string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	weight, ok := c.originWeights[originIP]
	return weight, ok
}

// GetLastUpdate returns when an origin was last updated
func (c *Client) GetLastUpdate(originIP string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.lastUpdate[originIP]
	return t, ok
}

// RefreshCache fetches the current pool state and updates the local cache
func (c *Client) RefreshCache(ctx context.Context) error {
	pool, err := c.GetPool(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, origin := range pool.Origins {
		weight := int(origin.Weight * 100)
		if !origin.Enabled {
			weight = 0
		}
		c.originWeights[origin.Address] = weight
	}

	return nil
}
