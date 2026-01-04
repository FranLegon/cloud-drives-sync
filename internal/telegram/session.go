package telegram

import (
	"context"
	"encoding/base64"
	"sync"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/gotd/td/session"
)

// MemorySession implements session.Storage using the User model
type MemorySession struct {
	user *model.User
	mux  sync.Mutex
}

func NewMemorySession(user *model.User) *MemorySession {
	return &MemorySession{
		user: user,
	}
}

func (s *MemorySession) LoadSession(ctx context.Context) ([]byte, error) {
	s.mux.Lock()
	defer s.mux.Unlock()

	if s.user.SessionData == "" {
		return nil, session.ErrNotFound
	}

	return base64.StdEncoding.DecodeString(s.user.SessionData)
}

func (s *MemorySession) StoreSession(ctx context.Context, data []byte) error {
	s.mux.Lock()
	defer s.mux.Unlock()

	s.user.SessionData = base64.StdEncoding.EncodeToString(data)
	return nil
}
