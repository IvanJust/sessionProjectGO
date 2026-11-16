package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"
)

type Service struct {
	users     Users
	sessions  Sessions
	ttl       time.Duration
	dummyHash string
}

func NewService(users Users, sessions Sessions, ttl time.Duration) (*Service, error) {
	dummy, err := HashPassword("dummy-password-for-timing-only")
	if err != nil {
		return nil, err
	}
	return &Service{users: users, sessions: sessions, ttl: ttl, dummyHash: dummy}, nil
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 255 || !strings.Contains(email, "@") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

func (s *Service) Register(ctx context.Context, email, password, ip, agent string) (Identity, string, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return Identity{}, "", err
	}
	if len(password) < 12 || len(password) > 1024 {
		return Identity{}, "", ErrInvalidPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Identity{}, "", err
	}
	user, err := s.users.Create(ctx, email, hash)
	if err != nil {
		return Identity{}, "", err
	}
	// The account remains valid if Redis fails: the user can sign in once it recovers.
	return s.createSession(ctx, user, ip, agent)
}

func (s *Service) Login(ctx context.Context, email, password, ip, agent string) (Identity, string, error) {
	email, err := normalizeEmail(email)
	if err != nil || len(password) > 1024 {
		return Identity{}, "", ErrCredentials
	}
	user, err := s.users.ByEmail(ctx, email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Identity{}, "", err
	}
	hash := user.PasswordHash
	if errors.Is(err, ErrNotFound) {
		hash = s.dummyHash
	}
	valid := CheckPassword(hash, password)
	if err != nil || !valid || !user.Active {
		return Identity{}, "", ErrCredentials
	}
	return s.createSession(ctx, user, ip, agent)
}

func (s *Service) createSession(ctx context.Context, user User, ip, agent string) (Identity, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Identity{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC()
	if len(agent) > 512 {
		agent = strings.ToValidUTF8(agent[:512], "")
	}
	session := Session{ID: tokenID(token), UserID: user.ID, CreatedAt: now, ExpiresAt: now.Add(s.ttl), IP: ip, UserAgent: agent}
	if err := s.sessions.Create(ctx, session, s.ttl); err != nil {
		return Identity{}, "", err
	}
	session.Current = true
	return Identity{User: user, Session: session}, token, nil
}

func tokenID(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func (s *Service) sessionByToken(ctx context.Context, token string) (Session, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return Session{}, ErrUnauthorized
	}
	session, err := s.sessions.Get(ctx, tokenID(token))
	if errors.Is(err, ErrNotFound) {
		return Session{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, err
	}
	if !time.Now().Before(session.ExpiresAt) {
		return Session{}, ErrUnauthorized
	}
	return session, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Identity, error) {
	session, err := s.sessionByToken(ctx, token)
	if err != nil {
		return Identity{}, err
	}
	user, err := s.users.ByID(ctx, session.UserID)
	if errors.Is(err, ErrNotFound) || (err == nil && !user.Active) {
		return Identity{}, ErrUnauthorized
	}
	if err != nil {
		return Identity{}, err
	}
	session.Current = true
	return Identity{User: user, Session: session}, nil
}

func (s *Service) ListSessions(ctx context.Context, identity Identity) ([]Session, error) {
	sessions, err := s.sessions.List(ctx, identity.User.ID)
	for i := range sessions {
		sessions[i].Current = sessions[i].ID == identity.Session.ID
	}
	return sessions, err
}

func (s *Service) Revoke(ctx context.Context, userID, sessionID string) error {
	if len(sessionID) != 64 {
		return ErrNotFound
	}
	if _, err := hex.DecodeString(sessionID); err != nil {
		return ErrNotFound
	}
	return s.sessions.Revoke(ctx, userID, sessionID)
}

func (s *Service) Logout(ctx context.Context, token string) error {
	session, err := s.sessionByToken(ctx, token)
	if errors.Is(err, ErrUnauthorized) {
		return nil
	}
	if err != nil {
		return err
	}
	err = s.sessions.Revoke(ctx, session.UserID, session.ID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (s *Service) LogoutAll(ctx context.Context, userID string) error {
	return s.sessions.RevokeAll(ctx, userID)
}
