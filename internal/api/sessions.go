package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// sessions holds short-lived operator sessions, so the browser keeps a cookie and never the
// admin key. In memory: a restart signs everyone out.
type sessions struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]time.Time
	now func() time.Time
}

func newSessions(ttl time.Duration) *sessions {
	return &sessions{ttl: ttl, m: map[string]time.Time{}, now: time.Now}
}

func (s *sessions) create() (token string, expires time.Time) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	token = hex.EncodeToString(b)
	expires = s.now().Add(s.ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	s.m[token] = expires
	return token, expires
}

func (s *sessions) valid(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[token]
	if !ok {
		return false
	}
	if !s.now().Before(exp) {
		delete(s.m, token)
		return false
	}
	return true
}

func (s *sessions) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, token)
}

func (s *sessions) sweep() {
	now := s.now()
	for t, exp := range s.m {
		if !now.Before(exp) {
			delete(s.m, t)
		}
	}
}
