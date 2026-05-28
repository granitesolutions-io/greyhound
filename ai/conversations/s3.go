package conversations

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/granitesolutions-io/greyhound/storage"
)

// S3Store persists conversations to an S3-compatible backend using a
// directory-based layout:
//
//	conversations/{id}/
//	  metadata.json        — static info (id, session_id, created_at)
//	  cost.json            — running total cost
//	  {timestamp}.json     — one per message exchange
type S3Store struct {
	store storage.Store
}

// NewS3Store creates a conversation store backed by the given storage.Store.
func NewS3Store(store storage.Store) *S3Store {
	return &S3Store{store: store}
}

func conversationDir(id string) string {
	return fmt.Sprintf("conversations/%s/", id)
}

func metadataKey(id string) string {
	return conversationDir(id) + "metadata.json"
}

func costKey(id string) string {
	return conversationDir(id) + "cost.json"
}

// metadata is the on-disk format for metadata.json.
type metadata struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	CreatedAt string `json:"created_at"`
}

// costFile is the on-disk format for cost.json.
type costFile struct {
	TotalCost float64 `json:"total_cost"`
}

// Create allocates a new Conversation with a generated UUID and persists it.
func (s *S3Store) Create() (*Conversation, error) {
	conv := &Conversation{
		ID:        newUUID(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// Write metadata.json
	meta := metadata{
		ID:        conv.ID,
		SessionID: conv.SessionID,
		CreatedAt: conv.CreatedAt,
	}
	if err := s.putJSON(metadataKey(conv.ID), meta); err != nil {
		return nil, fmt.Errorf("conversation: create metadata: %w", err)
	}

	// Write initial cost.json
	if err := s.putJSON(costKey(conv.ID), costFile{TotalCost: 0}); err != nil {
		return nil, fmt.Errorf("conversation: create cost: %w", err)
	}

	return conv, nil
}

// Get retrieves a conversation by ID. Returns nil, nil if not found.
func (s *S3Store) Get(id string) (*Conversation, error) {
	// Read metadata.json
	exists, err := s.store.Exists(metadataKey(id))
	if err != nil {
		return nil, fmt.Errorf("conversation: exists check: %w", err)
	}
	if !exists {
		return nil, nil
	}

	var meta metadata
	if err := s.getJSON(metadataKey(id), &meta); err != nil {
		return nil, fmt.Errorf("conversation: get metadata: %w", err)
	}

	// Read cost.json
	var cost costFile
	if err := s.getJSON(costKey(id), &cost); err != nil {
		return nil, fmt.Errorf("conversation: get cost: %w", err)
	}

	// List and read message files
	dir := conversationDir(id)
	keys, err := s.store.List(dir)
	if err != nil {
		return nil, fmt.Errorf("conversation: list messages: %w", err)
	}

	// Filter to only timestamp message files (exclude metadata.json and cost.json)
	var msgKeys []string
	for _, k := range keys {
		filename := strings.TrimPrefix(k, dir)
		if filename != "metadata.json" && filename != "cost.json" {
			msgKeys = append(msgKeys, k)
		}
	}
	sort.Strings(msgKeys)

	messages := make([]Message, 0, len(msgKeys))
	for _, k := range msgKeys {
		var msg Message
		if err := s.getJSON(k, &msg); err != nil {
			return nil, fmt.Errorf("conversation: get message %s: %w", k, err)
		}
		messages = append(messages, msg)
	}

	return &Conversation{
		ID:        meta.ID,
		SessionID: meta.SessionID,
		CreatedAt: meta.CreatedAt,
		Messages:  messages,
		TotalCost: cost.TotalCost,
	}, nil
}

// Save persists the conversation metadata and cost to S3.
// It does NOT rewrite individual message files.
func (s *S3Store) Save(conv *Conversation) error {
	// Write metadata.json (picks up session_id updates)
	meta := metadata{
		ID:        conv.ID,
		SessionID: conv.SessionID,
		CreatedAt: conv.CreatedAt,
	}
	if err := s.putJSON(metadataKey(conv.ID), meta); err != nil {
		return fmt.Errorf("conversation: save metadata: %w", err)
	}

	// Write cost.json
	if err := s.putJSON(costKey(conv.ID), costFile{TotalCost: conv.TotalCost}); err != nil {
		return fmt.Errorf("conversation: save cost: %w", err)
	}

	return nil
}

// AddMessage writes an individual message file and updates cost.json.
func (s *S3Store) AddMessage(conv *Conversation, msg Message) error {
	// Write {timestamp}.json
	msgKey := conversationDir(conv.ID) + msg.Timestamp + ".json"
	if err := s.putJSON(msgKey, msg); err != nil {
		return fmt.Errorf("conversation: add message: %w", err)
	}

	// Update in-memory state
	conv.Messages = append(conv.Messages, msg)
	conv.TotalCost = msg.TotalCost

	// Update cost.json
	if err := s.putJSON(costKey(conv.ID), costFile{TotalCost: conv.TotalCost}); err != nil {
		return fmt.Errorf("conversation: update cost: %w", err)
	}

	return nil
}

// putJSON marshals v and writes it to the given key.
func (s *S3Store) putJSON(key string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return s.store.Put(key, data)
}

// getJSON reads the given key and unmarshals it into v.
func (s *S3Store) getJSON(key string, v interface{}) error {
	data, err := s.store.Get(key)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	return json.Unmarshal(data, v)
}
