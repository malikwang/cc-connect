package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// UserTokenManager manages per-user API tokens with JSON file persistence.
type UserTokenManager struct {
	mu       sync.RWMutex
	tokens   map[string]string // userID -> API token
	filePath string
}

// NewUserTokenManager loads or creates a user token store at {dataDir}/user_tokens.json.
func NewUserTokenManager(dataDir string) (*UserTokenManager, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("token manager: mkdir %s: %w", dataDir, err)
	}
	tm := &UserTokenManager{
		tokens:   make(map[string]string),
		filePath: filepath.Join(dataDir, "user_tokens.json"),
	}
	data, err := os.ReadFile(tm.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return tm, nil
		}
		return nil, fmt.Errorf("token manager: read %s: %w", tm.filePath, err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &tm.tokens); err != nil {
			return nil, fmt.Errorf("token manager: parse %s: %w", tm.filePath, err)
		}
	}
	return tm, nil
}

// SetToken stores a token for the given user and persists to disk.
func (tm *UserTokenManager) SetToken(userID, token string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.tokens[userID] = token
	return tm.save()
}

// GetToken returns the token for the given user, or "" if not set.
func (tm *UserTokenManager) GetToken(userID string) string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.tokens[userID]
}

// ClearToken removes a user's token and persists to disk.
func (tm *UserTokenManager) ClearToken(userID string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.tokens, userID)
	return tm.save()
}

// HasToken returns true if the user has a token set.
func (tm *UserTokenManager) HasToken(userID string) bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	_, ok := tm.tokens[userID]
	return ok
}

func (tm *UserTokenManager) save() error {
	data, err := json.Marshal(tm.tokens)
	if err != nil {
		return fmt.Errorf("token manager: marshal: %w", err)
	}
	if err := os.WriteFile(tm.filePath, data, 0o600); err != nil {
		return fmt.Errorf("token manager: write %s: %w", tm.filePath, err)
	}
	return nil
}
