package auth

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrEmailExists     = errors.New("email already registered")
	ErrCredentials     = errors.New("invalid credentials")
	ErrUnauthorized    = errors.New("unauthorized")
	ErrInvalidEmail    = errors.New("invalid email")
	ErrInvalidPassword = errors.New("password must be between 12 and 1024 characters")
)

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	CreatedAt    time.Time `json:"created_at"`
	PasswordHash string    `json:"-"`
	Active       bool      `json:"-"`
}

type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Current   bool      `json:"current"`
}

type Identity struct {
	User    User    `json:"user"`
	Session Session `json:"session"`
}

type Users interface {
	Create(context.Context, string, string) (User, error)
	ByEmail(context.Context, string) (User, error)
	ByID(context.Context, string) (User, error)
}

type Sessions interface {
	Create(context.Context, Session, time.Duration) error
	Get(context.Context, string) (Session, error)
	List(context.Context, string) ([]Session, error)
	Revoke(context.Context, string, string) error
	RevokeAll(context.Context, string) error
}
