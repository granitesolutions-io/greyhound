package security

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Claims represents the authenticated user's identity.
type Claims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Issuer  string `json:"iss"`
}

// TokenVerifier validates a token and returns the associated claims.
type TokenVerifier interface {
	Verify(token string) (*Claims, error)
}

type claimsKey struct{}

// GetClaims extracts the authenticated claims from a request context.
func GetClaims(ctx context.Context) *Claims {
	claims, _ := ctx.Value(claimsKey{}).(*Claims)
	return claims
}

// cachedClaims holds verified claims with an expiry time.
type cachedClaims struct {
	claims    *Claims
	expiresAt time.Time
}

// Middleware returns HTTP middleware that verifies Bearer tokens using the given verifier.
// Verified tokens are cached for the specified TTL to avoid re-verifying on every request.
// A TTL of 0 defaults to 5 minutes.
func Middleware(verifier TokenVerifier, cacheTTL time.Duration) func(http.Handler) http.Handler {
	if cacheTTL == 0 {
		cacheTTL = 5 * time.Minute
	}

	var mu sync.RWMutex
	cache := make(map[string]cachedClaims)

	// Background cleanup every TTL interval
	go func() {
		ticker := time.NewTicker(cacheTTL)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			mu.Lock()
			for k, v := range cache {
				if now.After(v.expiresAt) {
					delete(cache, k)
				}
			}
			mu.Unlock()
		}
	}()

	lookup := func(token string) *Claims {
		mu.RLock()
		entry, ok := cache[token]
		mu.RUnlock()
		if ok && time.Now().Before(entry.expiresAt) {
			return entry.claims
		}
		return nil
	}

	store := func(token string, claims *Claims) {
		mu.Lock()
		cache[token] = cachedClaims{claims: claims, expiresAt: time.Now().Add(cacheTTL)}
		mu.Unlock()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				writeAuthError(w, "missing or invalid authorization header")
				return
			}

			token := strings.TrimPrefix(header, "Bearer ")

			// Check cache first
			if claims := lookup(token); claims != nil {
				ctx := context.WithValue(r.Context(), claimsKey{}, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Verify with provider
			claims, err := verifier.Verify(token)
			if err != nil {
				writeAuthError(w, "invalid or expired token")
				return
			}

			store(token, claims)

			ctx := context.WithValue(r.Context(), claimsKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAuth is a convenience wrapper that applies auth middleware to a single handler.
func RequireAuth(verifier TokenVerifier, cacheTTL time.Duration, handler http.HandlerFunc) http.HandlerFunc {
	wrapped := Middleware(verifier, cacheTTL)(handler)
	return wrapped.ServeHTTP
}

func writeAuthError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
