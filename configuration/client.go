package configuration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Entry is the API response for a key lookup.
type Entry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Format    string `json:"format,omitempty"`
	Secret    bool   `json:"secret,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// watchEvent is a WebSocket change notification from the server.
type watchEvent struct {
	Type      string `json:"type"`
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	Action    string `json:"action"`
	Namespace string `json:"namespace,omitempty"`
}

// watchMessage is sent to the server to subscribe to changes.
type watchMessage struct {
	Type string `json:"type"`
	Key  string `json:"key,omitempty"`
}

// cachedEntry stores a value along with which namespace it was resolved from.
type cachedEntry struct {
	value    string
	format   string
	secret   bool
	sourceNS string // "" = global, otherwise the namespace it came from
}

// cacheKey uniquely identifies a cached lookup.
type cacheKey struct {
	key    string
	format string
}

// ChangeHandler is called when a key's value changes.
// oldValue is empty on the first notification for a key.
type ChangeHandler func(key, oldValue, newValue string)

// Client is a read-only registry configuration client.
// It caches values in memory and listens for WebSocket updates
// so callers can call Get() frequently with no network overhead.
type Client struct {
	baseURL   string
	namespace string

	cache map[cacheKey]*cachedEntry
	mu    sync.RWMutex

	handlers map[string][]ChangeHandler
	handlerMu sync.RWMutex

	subscribed map[cacheKey]bool
	subMu      sync.Mutex

	conn   *websocket.Conn
	connMu sync.Mutex
	done   chan struct{}
}

// Option configures the Client.
type Option func(*Client)

// WithNamespace sets the default namespace for lookups.
// The client checks the namespace first, then falls back to global.
func WithNamespace(ns string) Option {
	return func(c *Client) { c.namespace = ns }
}

// Connect creates a registry client for the given service and environment.
// The service name is used as the registry namespace (e.g. "library"),
// so namespace-scoped values override globals automatically.
// "dev" connects to registry.granitesolutions.dev and "prod" (or
// "production") connects to registry.granitesolutions.io.  If
// registryURL is non-empty it overrides the environment-based URL.
func Connect(service, environment, registryURL string, opts ...Option) (*Client, error) {
	if registryURL == "" {
		registryURL = RegistryURL(environment)
	}
	return New(registryURL, append(opts, WithNamespace(service))...)
}

// RegistryURL returns the registry base URL for the given environment.
// "prod" and "production" resolve to registry.granitesolutions.io;
// everything else (including empty string) resolves to
// registry.granitesolutions.dev.
func RegistryURL(environment string) string {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "prod", "production":
		return "https://registry.granitesolutions.io"
	default:
		return "https://registry.granitesolutions.dev"
	}
}

// New creates a new registry client and connects to the WebSocket for updates.
func New(baseURL string, opts ...Option) (*Client, error) {
	c := &Client{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		cache:      make(map[cacheKey]*cachedEntry),
		handlers:   make(map[string][]ChangeHandler),
		subscribed: make(map[cacheKey]bool),
		done:       make(chan struct{}),
	}

	for _, opt := range opts {
		opt(c)
	}

	go c.connectLoop()

	return c, nil
}

// Get returns the value for a key with optional format.
// If the value is cached, it returns immediately from memory.
// On cache miss, it fetches from the server, caches the result,
// and subscribes to WebSocket updates for that key.
func (c *Client) Get(key string, format ...string) (string, error) {
	f := ""
	if len(format) > 0 {
		f = format[0]
	}

	ck := cacheKey{key: key, format: f}

	// Fast path: cache hit.
	c.mu.RLock()
	if entry, ok := c.cache[ck]; ok {
		c.mu.RUnlock()
		return entry.value, nil
	}
	c.mu.RUnlock()

	// Cache miss: fetch from server.
	entry, err := c.fetch(key, f)
	if err != nil {
		return "", err
	}

	// Cache the result.
	c.mu.Lock()
	c.cache[ck] = &cachedEntry{
		value:    entry.Value,
		format:   entry.Format,
		secret:   entry.Secret,
		sourceNS: entry.Namespace,
	}
	c.mu.Unlock()

	// Subscribe to updates for this key.
	c.subscribe(ck)

	return entry.Value, nil
}

// GetOrDefault returns the value for a key, or the default if the key is not found.
func (c *Client) GetOrDefault(key, defaultValue string, format ...string) string {
	value, err := c.Get(key, format...)
	if err != nil || value == "" {
		return defaultValue
	}
	return value
}

// Resolve returns the first non-empty value from, in priority order:
// CLI flag, environment variable, registry key, or the default value.
// The environment variable name is derived from the registry key by
// replacing dots and hyphens with underscores and uppercasing
// (e.g. "s3.access-key" becomes "S3_ACCESS_KEY").
// It is safe to call on a nil receiver; in that case the registry
// lookup is skipped.
func (c *Client) Resolve(key, flag, defaultValue string) string {
	return c.ResolveEnv(key, keyToEnvVar(key), flag, defaultValue)
}

// ResolveEnv is like Resolve but takes an explicit environment variable
// name for cases where the automatic conversion is not appropriate.
func (c *Client) ResolveEnv(key, envVar, flag, defaultValue string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	if c != nil {
		if v := c.GetOrDefault(key, ""); v != "" {
			return v
		}
	}
	return defaultValue
}

// keyToEnvVar converts a dot/hyphen-separated registry key to
// UPPER_SNAKE_CASE (e.g. "s3.access-key" → "S3_ACCESS_KEY").
func keyToEnvVar(key string) string {
	r := strings.NewReplacer(".", "_", "-", "_")
	return strings.ToUpper(r.Replace(key))
}

// OnChange registers a callback that fires when a key's value changes.
// The callback receives the key, old value, and new value.
// Multiple handlers can be registered for the same key.
func (c *Client) OnChange(key string, handler ChangeHandler) {
	c.handlerMu.Lock()
	c.handlers[key] = append(c.handlers[key], handler)
	c.handlerMu.Unlock()

	// Ensure we're subscribed to updates for this key.
	ck := cacheKey{key: key}
	c.subscribe(ck)
}

// Close shuts down the client and WebSocket connection.
func (c *Client) Close() {
	close(c.done)
	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.connMu.Unlock()
}

// --- HTTP fetch ---

func (c *Client) fetch(key, format string) (*Entry, error) {
	params := url.Values{}
	if format != "" {
		params.Set("format", format)
	}
	if c.namespace != "" {
		params.Set("namespace", c.namespace)
	}
	u := c.baseURL + "/api/setting/" + key
	if q := params.Encode(); q != "" {
		u += "?" + q
	}

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}

	// Sign the request so the registry can verify it came from a trusted service.
	ts := time.Now().Unix()
	req.Header.Set("X-Timestamp", fmt.Sprintf("%d", ts))
	req.Header.Set("X-Signature", Sign("GET", req.URL.Path, ts))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry fetch %s: %w", key, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("registry fetch %s: %s", key, strings.TrimSpace(string(body)))
	}

	var entry Entry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return nil, fmt.Errorf("registry decode %s: %w", key, err)
	}

	return &entry, nil
}

// --- WebSocket connection and event handling ---

func (c *Client) connectLoop() {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		err := c.connect()
		if err != nil {
			select {
			case <-c.done:
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		c.resubscribeAll()
		c.listen()

		// Connection lost — will reconnect.
		select {
		case <-c.done:
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *Client) connect() error {
	wsURL := strings.Replace(c.baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL += "/api/watch"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	return nil
}

func (c *Client) listen() {
	for {
		c.connMu.Lock()
		conn := c.conn
		c.connMu.Unlock()

		if conn == nil {
			return
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var event watchEvent
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}

		if event.Type == "change" {
			c.handleChange(event)
		}
	}
}

// handleChange processes a change event from the WebSocket.
// This is where the namespace overlay logic lives:
//
//   - If the event is for our namespace: always update the cache
//   - If the event is global (no namespace): only update if the cached
//     value was also from global. If our cache has a namespace-scoped
//     value, the global change must not overwrite it.
//   - If a namespace-scoped value is deleted: invalidate so the next
//     Get() re-fetches (and may fall back to global).
func (c *Client) handleChange(event watchEvent) {
	c.mu.Lock()

	var notifications []struct {
		key      string
		oldValue string
		newValue string
	}

	// Find all cache entries that match this key.
	for ck, cached := range c.cache {
		if !c.keyMatches(ck, event.Key) {
			continue
		}

		oldValue := cached.value

		switch event.Action {
		case "set":
			if event.Namespace == c.namespace && c.namespace != "" {
				// Our namespace changed — always update.
				cached.value = event.Value
				cached.sourceNS = event.Namespace
			} else if event.Namespace == "" {
				// Global changed — only update if we were using the global value.
				if cached.sourceNS == "" {
					cached.value = event.Value
				}
				// If sourceNS is our namespace, don't touch it.
			}

		case "delete":
			if event.Namespace == cached.sourceNS {
				// The source we were using got deleted — invalidate.
				// Next Get() will re-fetch and possibly fall back.
				delete(c.cache, ck)
			}
		}

		if cached.value != oldValue || event.Action == "delete" {
			notifications = append(notifications, struct {
				key      string
				oldValue string
				newValue string
			}{ck.key, oldValue, event.Value})
		}
	}

	c.mu.Unlock()

	// Fire callbacks outside the lock.
	for _, n := range notifications {
		c.fireHandlers(n.key, n.oldValue, n.newValue)
	}

	// Also fire for keys we have handlers for but aren't cached yet.
	c.handlerMu.RLock()
	hasHandler := len(c.handlers[event.Key]) > 0
	c.handlerMu.RUnlock()

	if hasHandler {
		alreadyNotified := false
		for _, n := range notifications {
			if n.key == event.Key {
				alreadyNotified = true
				break
			}
		}
		if !alreadyNotified {
			c.fireHandlers(event.Key, "", event.Value)
		}
	}
}

func (c *Client) fireHandlers(key, oldValue, newValue string) {
	c.handlerMu.RLock()
	handlers := c.handlers[key]
	c.handlerMu.RUnlock()

	for _, h := range handlers {
		h(key, oldValue, newValue)
	}
}

// keyMatches checks if a cache key corresponds to an event key.
func (c *Client) keyMatches(ck cacheKey, eventKey string) bool {
	if ck.key == eventKey {
		return true
	}
	// Handle the case where the cache key includes the format extension.
	if ck.format != "" && ck.key+"."+ck.format == eventKey {
		return true
	}
	// Handle the case where the event key includes the format extension.
	if ck.format != "" && ck.key == eventKey {
		return true
	}
	return false
}

// --- Subscription management ---

func (c *Client) subscribe(ck cacheKey) {
	c.subMu.Lock()
	defer c.subMu.Unlock()

	if c.subscribed[ck] {
		return
	}
	c.subscribed[ck] = true

	c.sendSubscribe(ck.key)
}

func (c *Client) resubscribeAll() {
	c.subMu.Lock()
	defer c.subMu.Unlock()

	for ck := range c.subscribed {
		c.sendSubscribe(ck.key)
	}
}

func (c *Client) sendSubscribe(key string) {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return
	}

	msg := watchMessage{Type: "subscribe", Key: key}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	conn.WriteMessage(websocket.TextMessage, data)
}
