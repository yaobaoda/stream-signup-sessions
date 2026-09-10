package streamauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// Creator is one media account. Passwords never leave this process in clear
// form; the salted digest is all the store keeps.
type Creator struct {
	ID     string
	Email  string
	Digest string
	Salt   string
}

// Session is the server-side record. The browser only ever holds the opaque ID.
type Session struct {
	ID        string
	CreatorID string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

var (
	ErrEmailTaken       = errors.New("email already registered")
	ErrBadCredentials   = errors.New("email or password does not match")
	ErrSessionNotFound  = errors.New("no live session for that id")
	ErrPasswordTooShort = errors.New("password must be at least 10 characters")
)

// Store holds creators and sessions in memory so the whole service ships as one
// binary with no database to stand up first.
type Store struct {
	mu        sync.Mutex
	byEmail   map[string]*Creator
	byID      map[string]*Creator
	sessions  map[string]*Session
	idemAccts map[string]string
	TTL       time.Duration
	Now       func() time.Time
}

func NewStore() *Store {
	return &Store{
		byEmail:   map[string]*Creator{},
		byID:      map[string]*Creator{},
		sessions:  map[string]*Session{},
		idemAccts: map[string]string{},
		TTL:       12 * time.Hour,
		Now:       time.Now,
	}
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// SignUp registers a creator. The idempotency key makes a retried POST return
// the account created the first time instead of a second one.
func (s *Store) SignUp(email, password, idempotencyKey string) (*Creator, error) {
	if len(password) < 10 {
		return nil, ErrPasswordTooShort
	}
	email = NormalizeEmail(email)

	s.mu.Lock()
	defer s.mu.Unlock()

	if idempotencyKey != "" {
		if id, seen := s.idemAccts[idempotencyKey]; seen {
			return s.byID[id], nil
		}
	}
	if _, taken := s.byEmail[email]; taken {
		return nil, ErrEmailTaken
	}

	salt := randomHex(16)
	c := &Creator{
		ID:     "cre_" + randomHex(8),
		Email:  email,
		Digest: digest(password, salt),
		Salt:   salt,
	}
	s.byEmail[email] = c
	s.byID[c.ID] = c
	if idempotencyKey != "" {
		s.idemAccts[idempotencyKey] = c.ID
	}
	return c, nil
}

// LogIn checks the password and opens a fresh server-side session.
func (s *Store) LogIn(email, password string) (*Session, error) {
	email = NormalizeEmail(email)

	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.byEmail[email]
	if !ok || digest(password, c.Salt) != c.Digest {
		return nil, ErrBadCredentials
	}
	now := s.Now()
	sess := &Session{
		ID:        "ses_" + randomHex(16),
		CreatorID: c.ID,
		IssuedAt:  now,
		ExpiresAt: now.Add(s.TTL),
	}
	s.sessions[sess.ID] = sess
	return sess, nil
}

// Resolve turns a cookie value back into a creator, dropping expired records.
func (s *Store) Resolve(sessionID string) (*Creator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	if !s.Now().Before(sess.ExpiresAt) {
		delete(s.sessions, sessionID)
		return nil, ErrSessionNotFound
	}
	return s.byID[sess.CreatorID], nil
}

// LogOut discards the record, which ends the session everywhere at once.
func (s *Store) LogOut(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
}

func digest(password, salt string) string {
	sum := sha256.Sum256([]byte(salt + ":" + password))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
