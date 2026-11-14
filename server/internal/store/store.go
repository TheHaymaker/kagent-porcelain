// Package store keeps Workbench's own tables. It is the only package that
// writes SQL; everything above it works with the types declared here.
package store

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a write would break a uniqueness or state rule.
var ErrConflict = errors.New("conflict")

// Store wraps a pool whose search path is the Workbench schema.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// New wraps pool. The pool's search_path must select the Workbench schema.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: time.Now}
}

// Pool exposes the pool for health checks.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// NewID returns a short random identifier with a type prefix, e.g. apr_….
func NewID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23514") {
		return fmt.Errorf("%w: %s", ErrConflict, pg.Message)
	}
	return err
}

// tx runs fn in a transaction.
func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, fn)
}

// Page bounds a listing.
type Page struct {
	Size   int
	Offset int
}

func (p Page) limit() int {
	if p.Size <= 0 || p.Size > 200 {
		return 50
	}
	return p.Size
}
