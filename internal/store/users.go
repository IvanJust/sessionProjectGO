package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"sessionProject/internal/auth"
)

type Users struct{ pool *pgxpool.Pool }

func NewUsers(pool *pgxpool.Pool) *Users { return &Users{pool: pool} }

func scanUser(row pgx.Row) (auth.User, error) {
	var user auth.User
	err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Active, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return auth.User{}, auth.ErrEmailExists
	}
	return user, err
}

func (s *Users) Create(ctx context.Context, email, hash string) (auth.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1,$2) RETURNING id::text, email, password_hash, is_active, created_at`, email, hash))
}

func (s *Users) ByEmail(ctx context.Context, email string) (auth.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT id::text, email, password_hash, is_active, created_at FROM users WHERE lower(email)=$1`, email))
}

func (s *Users) ByID(ctx context.Context, id string) (auth.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT id::text, email, password_hash, is_active, created_at FROM users WHERE id=$1`, id))
}
