package conversations

import (
	"encoding/json"
	"fmt"

	"github.com/granitesolutions-io/greyhound/storage"
)

// S3Store persists conversations to an S3-compatible backend.
type S3Store struct {
	store storage.Store
}

// NewS3Store creates a conversation store backed by the given storage.Store.
func NewS3Store(store storage.Store) *S3Store {
	return &S3Store{store: store}
}

func conversationKey(id string) string {
	return fmt.Sprintf("conversations/%s.json", id)
}

// Create allocates a new Conversation with a generated UUID and persists it.
func (s *S3Store) Create() (*Conversation, error) {
	conv := &Conversation{ID: newUUID()}

	data, err := json.Marshal(conv)
	if err != nil {
		return nil, fmt.Errorf("conversation: marshal: %w", err)
	}

	if err := s.store.Put(conversationKey(conv.ID), data); err != nil {
		return nil, fmt.Errorf("conversation: create: %w", err)
	}

	return conv, nil
}

// Get retrieves a conversation by ID. Returns nil, nil if not found.
func (s *S3Store) Get(id string) (*Conversation, error) {
	key := conversationKey(id)

	exists, err := s.store.Exists(key)
	if err != nil {
		return nil, fmt.Errorf("conversation: exists check: %w", err)
	}
	if !exists {
		return nil, nil
	}

	data, err := s.store.Get(key)
	if err != nil {
		return nil, fmt.Errorf("conversation: get: %w", err)
	}

	var conv Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("conversation: unmarshal: %w", err)
	}

	return &conv, nil
}

// Save persists the conversation to S3.
func (s *S3Store) Save(conv *Conversation) error {
	data, err := json.Marshal(conv)
	if err != nil {
		return fmt.Errorf("conversation: marshal: %w", err)
	}

	if err := s.store.Put(conversationKey(conv.ID), data); err != nil {
		return fmt.Errorf("conversation: save: %w", err)
	}

	return nil
}
