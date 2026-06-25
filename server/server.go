package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// InfoHandler returns a handler that responds with {"name":"…","version":"…"}.
func InfoHandler(name, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"name": name, "version": version})
	}
}

// HealthHandler responds with {"status":"ok"}.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CitadelProxy returns a handler that proxies to citadelURL+path,
// injecting "service": serviceName into POST/PUT JSON bodies when non-empty.
func CitadelProxy(citadelURL, path, serviceName string) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var body io.Reader = r.Body
		if serviceName != "" && (r.Method == http.MethodPost || r.Method == http.MethodPut) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read request", http.StatusBadRequest)
				return
			}
			var data map[string]any
			if err := json.Unmarshal(raw, &data); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			data["service"] = serviceName
			modified, _ := json.Marshal(data)
			body = bytes.NewReader(modified)
		}

		req, err := http.NewRequest(r.Method, citadelURL+path, body)
		if err != nil {
			http.Error(w, "failed to create request", http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "citadel unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// JSON writes a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Error writes a JSON error response: {"error":"message"}.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}
