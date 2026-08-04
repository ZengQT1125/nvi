package gateway

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"nvidia-api-gateway/pkg/models"
)

type conversationAffinityRecord struct {
	PreferredKey string
	ExpiresAt    time.Time
}

type conversationAffinityStore struct {
	mu    sync.Mutex
	items map[string]conversationAffinityRecord
}

var globalConversationAffinityStore = &conversationAffinityStore{items: map[string]conversationAffinityRecord{}}

const conversationAffinityTTL = 2 * time.Hour

func resolveConversationAffinityID(headerValue string, rawBody []byte, masterKey *models.MasterKey) string {
	if value := strings.TrimSpace(headerValue); value != "" {
		return "cid:" + value
	}
	if seed := extractConversationAffinitySeed(rawBody); seed != "" {
		return seed
	}
	if masterKey != nil && masterKey.ID > 0 {
		return fmt.Sprintf("mk:%d", masterKey.ID)
	}
	if len(rawBody) == 0 {
		return ""
	}
	sum := sha1.Sum(rawBody)
	return "body:" + hex.EncodeToString(sum[:8])
}

func extractConversationAffinitySeed(rawBody []byte) string {
	if len(rawBody) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return ""
	}
	if previous := strings.TrimSpace(stringValue(payload["previous_response_id"])); previous != "" {
		return "resp:" + previous
	}
	if user := strings.TrimSpace(stringValue(payload["user"])); user != "" {
		return "user:" + user
	}
	return ""
}

func (s *conversationAffinityStore) get(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if s == nil || id == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.items[id]
	if !ok {
		return "", false
	}
	if !record.ExpiresAt.IsZero() && time.Now().After(record.ExpiresAt) {
		delete(s.items, id)
		return "", false
	}
	return strings.TrimSpace(record.PreferredKey), strings.TrimSpace(record.PreferredKey) != ""
}

func (s *conversationAffinityStore) set(id, key string) {
	id = strings.TrimSpace(id)
	key = strings.TrimSpace(key)
	if s == nil || id == "" || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = conversationAffinityRecord{PreferredKey: key, ExpiresAt: time.Now().Add(conversationAffinityTTL)}
}

func (s *conversationAffinityStore) clear(id, key string) {
	id = strings.TrimSpace(id)
	key = strings.TrimSpace(key)
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.items[id]
	if !ok {
		return
	}
	if key != "" && strings.TrimSpace(record.PreferredKey) != key {
		return
	}
	delete(s.items, id)
}
