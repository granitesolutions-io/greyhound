package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/granitesolutions-io/greyhound/configuration"
)

// UsageEvent represents a single usage record to send to tariff.
type UsageEvent struct {
	CustomerID   string          `json:"customerId"`
	ServiceKey   string          `json:"serviceKey"`
	Quantity     float64         `json:"quantity"`
	Unit         string          `json:"unit"`
	ProviderCost float64         `json:"providerCost,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
}

// Client sends usage events to tariff asynchronously.
// Calling Record on a nil Client is a safe no-op.
type Client struct {
	baseURL string
	service string // default serviceKey if event.ServiceKey is empty
	queue   chan UsageEvent
	done    chan struct{}
}

// Connect creates a billing client for the given service and environment.
// If tariffURL is non-empty it overrides the environment-based URL.
func Connect(service, environment, tariffURL string) (*Client, error) {
	if tariffURL == "" {
		tariffURL = TariffURL(environment)
	}

	c := &Client{
		baseURL: strings.TrimSuffix(tariffURL, "/"),
		service: service,
		queue:   make(chan UsageEvent, 256),
		done:    make(chan struct{}),
	}

	go c.worker()

	return c, nil
}

// TariffURL returns the billing base URL for the given environment.
// "prod" and "production" resolve to billing.granitesolutions.io;
// everything else (including empty string) resolves to billing.granitesolutions.dev.
func TariffURL(environment string) string {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "prod", "production":
		return "https://billing.granitesolutions.io"
	default:
		return "https://billing.granitesolutions.dev"
	}
}

// Record queues a usage event for async delivery to tariff.
// It is safe to call on a nil receiver and never blocks the caller.
// If the queue is full the event is silently dropped.
func (c *Client) Record(event UsageEvent) {
	if c == nil {
		return
	}

	// Apply default service key if the event doesn't specify one.
	if event.ServiceKey == "" {
		event.ServiceKey = c.service
	}

	select {
	case c.queue <- event:
	default:
		log.Printf("[billing] queue full, dropping event for %s", event.CustomerID)
	}
}

// Close drains remaining events and shuts down the background worker.
// It is safe to call on a nil receiver.
func (c *Client) Close() {
	if c == nil {
		return
	}
	close(c.done)

	// Drain any remaining events.
	for {
		select {
		case event := <-c.queue:
			c.post(event)
		default:
			return
		}
	}
}

// worker is the background goroutine that drains the queue.
func (c *Client) worker() {
	for {
		select {
		case event := <-c.queue:
			c.post(event)
		case <-c.done:
			return
		}
	}
}

// post sends a single usage event to tariff.
func (c *Client) post(event UsageEvent) {
	body, err := json.Marshal(event)
	if err != nil {
		log.Printf("[billing] marshal error: %s", err)
		return
	}

	path := "/api/usage"
	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		log.Printf("[billing] request error: %s", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	// Sign the request identically to configuration.Client.fetch().
	ts := time.Now().Unix()
	req.Header.Set("X-Timestamp", fmt.Sprintf("%d", ts))
	req.Header.Set("X-Signature", configuration.Sign("POST", path, ts))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[billing] tariff unavailable: %s", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("[billing] tariff returned %d for %s/%s", resp.StatusCode, event.CustomerID, event.ServiceKey)
	}
}
