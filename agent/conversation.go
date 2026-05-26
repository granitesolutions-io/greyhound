package agent

import (
	"crypto/rand"
	"fmt"
	"sync"

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

// ConversationStore is the interface for conversation persistence.
type ConversationStore interface {
	Create() (*Conversation, error)
	Get(id string) (*Conversation, error)
	Save(conv *Conversation) error
}

// NewConversationStore creates a ConversationStore backed by S3 if a storage.Store
// is provided, or an in-memory store otherwise.
func NewConversationStore(store storage.Store) ConversationStore {
	if store != nil {
		return NewS3ConversationStore(store)
	}
	return NewMemoryConversationStore()
}

// MemoryConversationStore is a thread-safe in-memory store for conversations.
type MemoryConversationStore struct {
	mu            sync.Mutex
	conversations map[string]*Conversation
}

// NewMemoryConversationStore creates an empty MemoryConversationStore.
func NewMemoryConversationStore() *MemoryConversationStore {
	return &MemoryConversationStore{
		conversations: make(map[string]*Conversation),
	}
}

// Create allocates a new Conversation with a generated UUID and stores it.
func (s *MemoryConversationStore) Create() (*Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := newUUID()
	c := &Conversation{ID: id}
	s.conversations[id] = c
	return c, nil
}

// Get returns the Conversation for the given ID, or nil if not found.
func (s *MemoryConversationStore) Get(id string) (*Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversations[id], nil
}

// Save persists the conversation (no-op for in-memory store since it's already in the map).
func (s *MemoryConversationStore) Save(conv *Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conversations[conv.ID] = conv
	return nil
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
