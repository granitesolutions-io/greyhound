package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/granitesolutions-io/greyhound/cli"
)

// App holds the identity and lifecycle for a greyhound-based service.
type App struct {
	Name    string // display name, e.g. "Landlord"
	Version string // version string, e.g. "1.2.3"
	Banner  string // ASCII art banner text
	Color   string // optional primary color override (e.g. "#6366F1")

	initOnce sync.Once
}

// New creates a new App and immediately initializes it (prints the
// startup banner and configures logging).  Services that embed App
// can call New so the banner appears before any configuration output.
func New(name, version, banner string) *App {
	a := &App{Name: name, Version: version, Banner: banner}
	a.Init()
	return a
}

// Init sets up structured logging and prints the startup header.
// It is safe to call multiple times; only the first call takes effect.
func (a *App) Init() {
	a.initOnce.Do(func() {
		log.SetFlags(0)
		log.SetOutput(timestampWriter{})

		if a.Color != "" {
			cli.SetPrimaryColor(a.Color)
		}

		cli.PrintHeader(a.Banner, a.Version)
	})
}

// ListenAndWait starts an HTTP server on the given port, prints ready messages,
// then blocks until SIGINT or SIGTERM is received. It calls the optional cleanup
// function before performing a graceful shutdown with a 5-second timeout.
func (a *App) ListenAndWait(port int, handler http.Handler, cleanup ...func()) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		cli.PrintError("Failed to bind port %d: %s", port, err)
		os.Exit(1)
	}

	srv := &http.Server{Handler: handler}

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %s", err)
		}
	}()

	cli.PrintSuccess("Started http server on port %d.", port)
	cli.PrintSuccess("%s v%s is ready!", a.Name, a.Version)
	fmt.Println()

	// Block until shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	log.Printf("Received %s, shutting down...", sig)

	for _, fn := range cleanup {
		fn()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %s", err)
	}
	log.Printf("Server stopped.")
}

// timestampWriter implements io.Writer to prefix log lines with RFC3339 timestamps.
type timestampWriter struct{}

func (timestampWriter) Write(p []byte) (int, error) {
	ts := time.Now().UTC().Format(time.RFC3339)
	return fmt.Fprintf(os.Stderr, "%s  %s", ts, p)
}

// --- Environment helpers ---

// EnvOr returns flag if non-empty, otherwise the value of the named environment variable.
func EnvOr(flag, envKey string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv(envKey)
}

// EnvOrInt returns flag if non-zero, otherwise the named environment variable parsed as int.
func EnvOrInt(flag int, envKey string) int {
	if flag != 0 {
		return flag
	}
	if v := os.Getenv(envKey); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// EnvOrIntDefault is like EnvOrInt but returns defaultVal instead of 0 when
// neither flag nor env var is set.
func EnvOrIntDefault(flag int, envKey string, defaultVal int) int {
	if flag != 0 {
		return flag
	}
	if v := os.Getenv(envKey); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

// ParsePeers splits a comma-separated string into trimmed, non-empty strings.
func ParsePeers(s string) []string {
	if s == "" {
		return nil
	}
	var peers []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			peers = append(peers, p)
		}
	}
	return peers
}
