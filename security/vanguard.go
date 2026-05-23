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

// DefaultVanguardURL is the default Vanguard service endpoint.
const DefaultVanguardURL = "https://accounts.granitesolutions.io"

// ResolveVanguardURL returns the Vanguard URL to use, checking in order:
// 1. CLI flag value (if non-empty)
// 2. VANGUARD_URL environment variable (if set)
// 3. Default: https://accounts.granitesolutions.io
func ResolveVanguardURL(flagValue string) string {
	if flagValue != "" {
		return strings.TrimRight(flagValue, "/")
	}
	if env := os.Getenv("VANGUARD_URL"); env != "" {
		return strings.TrimRight(env, "/")
	}
	return DefaultVanguardURL
}

// Vanguard implements TokenVerifier by calling a Vanguard auth service.
type Vanguard struct {
	URL    string
	client *http.Client
}

// NewVanguard creates a verifier that validates tokens against the given Vanguard URL.
func NewVanguard(url string) *Vanguard {
	return &Vanguard{
		URL: strings.TrimRight(url, "/"),
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// vanguardResponse is the JSON response from /api/tokens/verify.
type vanguardResponse struct {
	Valid  bool            `json:"valid"`
	Error  string          `json:"error,omitempty"`
	Claims json.RawMessage `json:"claims,omitempty"`
}

type vanguardClaims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Issuer  string `json:"iss"`
}

// Verify calls Vanguard's /api/tokens/verify endpoint to validate the token.
func (v *Vanguard) Verify(token string) (*Claims, error) {
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := v.client.Post(v.URL+"/api/tokens/verify", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to reach vanguard: %w", err)
	}
	defer resp.Body.Close()

	var result vanguardResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode vanguard response: %w", err)
	}

	if !result.Valid {
		return nil, fmt.Errorf("%s", result.Error)
	}

	var vc vanguardClaims
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
