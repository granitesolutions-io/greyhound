package conversations

import "sync"

// MemoryStore is a thread-safe in-memory store for conversations.
type MemoryStore struct {
	mu            sync.Mutex
	conversations map[string]*Conversation
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		conversations: make(map[string]*Conversation),
	}
}

// Create allocates a new Conversation with a generated UUID and stores it.
func (s *MemoryStore) Create() (*Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := newUUID()
	c := &Conversation{ID: id}
	s.conversations[id] = c
	return c, nil
}

// Get returns the Conversation for the given ID, or nil if not found.
func (s *MemoryStore) Get(id string) (*Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversations[id], nil
}

// Save persists the conversation (no-op for in-memory store since it's already in the map).
func (s *MemoryStore) Save(conv *Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conversations[conv.ID] = conv
	return nil
}

// AddMessage appends a message to the conversation and updates the total cost.
func (s *MemoryStore) AddMessage(conv *Conversation, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conv.Messages = append(conv.Messages, msg)
	conv.TotalCost = msg.TotalCost
	return nil
}
