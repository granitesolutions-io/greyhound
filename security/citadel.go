package security

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultCitadelURL is the default Citadel service endpoint.
const DefaultCitadelURL = "https://accounts.granitesolutions.io"

// ResolveCitadelURL returns the Citadel URL to use, checking in order:
// 1. CLI flag value (if non-empty)
// 2. CITADEL_URL environment variable (if set)
// 3. Default: https://accounts.granitesolutions.io
func ResolveCitadelURL(flagValue string) string {
	if flagValue != "" {
		return strings.TrimRight(flagValue, "/")
	}
	if env := os.Getenv("CITADEL_URL"); env != "" {
		return strings.TrimRight(env, "/")
	}
	return DefaultCitadelURL
}

// Citadel implements TokenVerifier by calling a Citadel auth service.
type Citadel struct {
	URL    string
	client *http.Client
}

// NewCitadel creates a verifier that validates tokens against the given Citadel URL.
func NewCitadel(url string) *Citadel {
	return &Citadel{
		URL: strings.TrimRight(url, "/"),
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// citadelResponse is the JSON response from /api/tokens/verify.
type citadelResponse struct {
	Valid  bool            `json:"valid"`
	Error  string          `json:"error,omitempty"`
	Claims json.RawMessage `json:"claims,omitempty"`
}

type citadelClaims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Issuer  string `json:"iss"`
}

// Verify calls Citadel's /api/tokens/verify endpoint to validate the token.
func (c *Citadel) Verify(token string) (*Claims, error) {
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.client.Post(c.URL+"/api/tokens/verify", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to reach citadel: %w", err)
	}
	defer resp.Body.Close()

	var result citadelResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode citadel response: %w", err)
	}

	if !result.Valid {
		return nil, fmt.Errorf("%s", result.Error)
	}

	var vc citadelClaims
	if err := json.Unmarshal(result.Claims, &vc); err != nil {
		return nil, fmt.Errorf("failed to decode claims: %w", err)
	}

	return &Claims{
		Subject: vc.Subject,
		Email:   vc.Email,
		Name:    vc.Name,
		Issuer:  vc.Issuer,
	}, nil
}
