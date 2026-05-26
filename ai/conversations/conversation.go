package conversations

import (
	"crypto/rand"
	"fmt"

	"github.com/granitesolutions-io/greyhound/storage"
)

// Message represents a single message in a conversation.
type Message struct {
	Role    string `json:"role"`    // "user" or "assistant"
	Content string `json:"content"`
}

// Conversation tracks a multi-turn conversation with Claude.
type Conversation struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"` // Claude CLI session ID (set after first Run)
	Messages  []Message `json:"messages"`
}

// Store is the interface for conversation persistence.
type Store interface {
	Create() (*Conversation, error)
	Get(id string) (*Conversation, error)
	Save(conv *Conversation) error
}

// New creates a Store backed by S3 if a storage.Store is provided,
// or an in-memory store otherwise.
func New(store storage.Store) Store {
	if store != nil {
		return NewS3Store(store)
	}
	return NewMemoryStore()
}

// newUUID generates a random UUID v4.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 2
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
