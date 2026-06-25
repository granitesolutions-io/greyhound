# Greyhound

Shared Go libraries for GraniteSolutions services. Provides common building blocks for CLI output, HTTP service lifecycle, distributed clustering, storage, security, configuration, email, encryption, and AI agent integration.

```
go get github.com/granitesolutions-io/greyhound@latest
```

## Packages

### `app` -- Service Lifecycle

Bootstraps HTTP services with structured logging, startup banners, and graceful shutdown on SIGINT/SIGTERM.

```go
import "github.com/granitesolutions-io/greyhound/app"

a := &app.App{
    Name:    "MyService",
    Version: "1.0.0",
    Banner:  myBanner,
    Color:   "#6366F1", // optional primary color override
}
a.Init() // sets up logging, prints startup header

mux := http.NewServeMux()
mux.HandleFunc("/api/health", healthHandler)

// Blocks until SIGINT/SIGTERM, then gracefully shuts down.
// Optional cleanup functions run before shutdown.
a.ListenAndWait(8080, mux, func() {
    db.Close()
})
```

Also provides environment variable helpers:

```go
// Returns flag value if non-empty, otherwise reads from env var.
host := app.EnvOr(flagHost, "DB_HOST")
port := app.EnvOrInt(flagPort, "DB_PORT")
port := app.EnvOrIntDefault(flagPort, "DB_PORT", 5432)

// Splits "host1,host2,host3" into []string.
peers := app.ParsePeers(peerList)
```

---

### `cli` -- Terminal Output

TTY-aware styled output using [Lipgloss](https://github.com/charmbracelet/lipgloss). Automatically falls back to plain text with timestamps when stdout is not a terminal (e.g. in Docker logs or CI).

```go
import "github.com/granitesolutions-io/greyhound/cli"

cli.PrintSuccess("Server started on port %d.", 8080)
cli.PrintError("Failed to connect: %s", err)
cli.PrintWarning("Cache miss for key %s", key)
cli.PrintInfo("Processing %d items", count)
cli.PrintKeyValue("Storage", "s3")
cli.PrintHeader(banner, version)
```

Exported styles can be used directly with Lipgloss:

```go
fmt.Println(cli.TitleStyle.Render("My Title"))
fmt.Println(cli.Divider())
fmt.Println(cli.Highlight("important text"))
fmt.Println(cli.Muted("secondary info"))
fmt.Println(cli.Bold("bold text"))
```

Override the primary color before any output:

```go
cli.SetPrimaryColor("#FF6B6B")
```

---

### `storage` -- Pluggable Key-Value Storage

Abstract storage interface with filesystem and S3-compatible backends. The backend is selected automatically based on whether S3 credentials are provided.

```go
import "github.com/granitesolutions-io/greyhound/storage"

store, err := storage.New(storage.Config{
    BaseDir:   "/data/myapp",     // used for filesystem backend
    Bucket:    "my-bucket",       // S3 bucket
    Prefix:    "myapp/",          // optional S3 key prefix
    Region:    "us-east-1",
    Endpoint:  "https://...",     // for DigitalOcean Spaces, MinIO, etc.
    AccessKey: "...",
    SecretKey: "...",
})

// All operations work the same regardless of backend.
store.Put("users/alice.json", data)
data, err := store.Get("users/alice.json")
exists, err := store.Exists("users/alice.json")
items, err := store.List("users/")
info, err := store.Stat("users/alice.json")
store.Delete("users/alice.json")
```

For filesystem-only usage:

```go
store := storage.NewFileStore("/data/myapp")
```

The filesystem backend uses atomic writes (temp file + rename) for safety.

---

### `cluster` -- Distributed Clustering

Two-tier clustering built on [HashiCorp Memberlist](https://github.com/hashicorp/memberlist) (gossip) and [Raft](https://github.com/hashicorp/raft) (consensus).

#### Basic gossip cluster

Peer discovery with topic-based messaging. Supports mDNS, HTTP-based discovery (Kubernetes), and static peer lists.

```go
import "github.com/granitesolutions-io/greyhound/cluster"

c := &cluster.Cluster{}

c.OnJoin(func(m cluster.Member) {
    log.Printf("Member joined: %s", m.Name)
})
c.OnLeave(func(m cluster.Member) {
    log.Printf("Member left: %s", m.Name)
})
c.OnMessage("my.topic", func(from cluster.Member, data []byte) {
    log.Printf("Got message from %s: %s", from.Name, data)
})

cfg := cluster.DefaultClusterConfig()
cfg.BindPort = 7946
cfg.Peers = []string{"peer1:7946", "peer2:7946"}
c.Start(cfg)
defer c.Stop()

// Broadcast to all members.
c.Broadcast("my.topic", []byte("hello"))

// Send to a specific member.
c.Send("peer1", "my.topic", []byte("hello"))

// Expose peer list via HTTP (for Kubernetes headless service discovery).
mux.HandleFunc("/api/cluster/peers", c.ClusterPeersHandler())
```

#### Partitioned cluster with Raft

Adds leader election, consistent hashing, and shard assignment on top of the gossip layer.

```go
pc := &cluster.PartitionedCluster{}

cfg := cluster.PartitionedClusterConfig{
    ClusterConfig: cluster.DefaultClusterConfig(),
    DataDir:       "/data/raft",
    TotalShards:   64,
}
pc.Start(cfg)
defer pc.Stop()

// Check shard ownership.
if pc.OwnsKey("user:alice") {
    // This node is responsible for this key.
}

// Replicate state changes via Raft.
pc.Replicate("users", "create", userData)
pc.OnApply("users", func(op string, data []byte) {
    // Applied on all nodes.
})
```

---

### `security` -- Authentication Middleware

HTTP middleware for Bearer token verification with response caching. Includes a built-in client for the [Citadel](https://github.com/granitesolutions-io/citadel) auth service.

```go
import "github.com/granitesolutions-io/greyhound/security"

// Create a Citadel token verifier.
verifier := security.NewCitadel("https://accounts.granitesolutions.io")

// Wrap handlers with auth middleware (caches verifications for 5 min by default).
auth := security.Middleware(verifier, 0)
mux.Handle("/api/data", auth(myHandler))

// Or wrap individual handlers.
mux.HandleFunc("/api/data", security.RequireAuth(verifier, 0, myHandler))

// Access claims in handlers.
func myHandler(w http.ResponseWriter, r *http.Request) {
    claims := security.GetClaims(r.Context())
    fmt.Println(claims.Email, claims.AccountID)
}
```

Implement `TokenVerifier` for custom auth backends:

```go
type TokenVerifier interface {
    Verify(token string) (*Claims, error)
}
```

---

### `configuration` -- Remote Configuration Client

Client for a configuration registry service with in-memory caching and WebSocket-based change notifications.

```go
import "github.com/granitesolutions-io/greyhound/configuration"

client := configuration.New(
    "https://registry.example.com",
    configuration.WithNamespace("my-service"),
)
defer client.Close()

// Reads from cache, falls back to HTTP.
val := client.Get("database.host")
val := client.GetOrDefault("log.level", "info")

// React to changes in real time.
client.OnChange("feature.flags", func(key, value string) {
    log.Printf("Config changed: %s = %s", key, value)
})
```

Namespace-scoped values take precedence over global fallbacks.

---

### `email` -- SMTP Email

Simple SMTP email sender with graceful degradation when not configured.

```go
import "github.com/granitesolutions-io/greyhound/email"

mailer := email.New(email.Config{
    Host:     "smtp.example.com",
    Port:     587,
    Username: "user",
    Password: "pass",
    From:     "noreply@example.com",
    FromName: "My App",
})

if mailer.IsConfigured() {
    mailer.Send("user@example.com", "Welcome", "<h1>Hello!</h1>")
}
```

Returns a no-op sender when credentials are missing, so callers don't need nil checks.

---

### `encryption` -- AES-256-GCM Encryption

Symmetric encryption utilities using AES-256-GCM with random nonces.

```go
import "github.com/granitesolutions-io/greyhound/encryption"

key := encryption.DeriveKey("my-secret-passphrase") // SHA-256 derived
// Or: key := encryption.DeriveKey("64-char-hex-string")

ciphertext, err := encryption.Encrypt(key, []byte("sensitive data"))
// ciphertext is base64-encoded with prepended nonce

plaintext, err := encryption.Decrypt(key, ciphertext)
```

---

### `ai` -- Claude AI Integration

Wraps the Claude CLI for agent-based AI interactions with MCP server support, session management, and conversation persistence.

#### Agent

```go
import "github.com/granitesolutions-io/greyhound/ai/agent"

a := &agent.Agent{
    Model:    "claude-sonnet-4-20250514",
    MaxTurns: 10,
    MCPServers: []agent.MCPServer{
        {Name: "my-tools", Args: []string{"node", "server.js"}},
    },
    OnEvent: func(e agent.Event) {
        switch e.Type {
        case agent.EventResult:
            fmt.Println(e.Text)
        case agent.EventToolUse:
            fmt.Printf("Tool: %s\n", e.ToolName)
        }
    },
}

result, err := a.Run("Summarize the latest data")
fmt.Println(result.RawOutput)
fmt.Printf("Cost: $%.4f\n", result.Cost)
```

#### Skills

Template-based prompt system loaded from embedded filesystems:

```go
import "github.com/granitesolutions-io/greyhound/ai/agent"

skills, _ := agent.ListSkills(skillsFS, "skills")
skill, _ := agent.LoadSkill(skillsFS, "skills", "analyze")
prompt, _ := agent.RenderPrompt(skill, templateData)
```

#### Conversations

Multi-turn conversation tracking with persistent or in-memory storage:

```go
import "github.com/granitesolutions-io/greyhound/ai/conversations"

store := conversations.New(s3Store) // or nil for in-memory

conv, _ := store.Create()
store.AddMessage(conv, conversations.Message{
    User:      "Hello",
    Assistant: "Hi there!",
    Cost:      0.0012,
})
store.Save(conv)

// Resume later.
conv, _ = store.Get(conv.ID)
```

## License

Proprietary -- GraniteSolutions.
