// Package store keeps books, extracted story knowledge and reader state in
// SQLite.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

const schemaVersion = 1

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	switch {
	case v == schemaVersion:
		return nil
	case v > schemaVersion:
		return fmt.Errorf("store: database schema v%d is newer than this gmesh (v%d)", v, schemaVersion)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("store: create schema: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

type User struct {
	ID      int64
	Name    string
	IsAdmin bool
}

// EnsureUser returns the named user, creating it if needed. The first user
// ever created is the admin.
func (s *Store) EnsureUser(ctx context.Context, name string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	u := User{Name: name}
	err = tx.QueryRowContext(ctx, "SELECT id, is_admin FROM users WHERE name = ?", name).Scan(&u.ID, &u.IsAdmin)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return User{}, err
	}
	u.IsAdmin = n == 0
	res, err := tx.ExecContext(ctx, "INSERT INTO users (name, is_admin) VALUES (?, ?)", name, u.IsAdmin)
	if err != nil {
		return User{}, err
	}
	if u.ID, err = res.LastInsertId(); err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}

// UserByName returns ok=false if there is no such user.
func (s *Store) UserByName(ctx context.Context, name string) (User, bool, error) {
	u := User{Name: name}
	err := s.db.QueryRowContext(ctx, "SELECT id, is_admin FROM users WHERE name = ?", name).Scan(&u.ID, &u.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	return u, err == nil, err
}

// EnsureSeries returns the id of the named series, creating it if needed.
func (s *Store) EnsureSeries(ctx context.Context, name string) (int64, error) {
	if _, err := s.db.ExecContext(ctx, "INSERT INTO series (name) VALUES (?) ON CONFLICT(name) DO NOTHING", name); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM series WHERE name = ?", name).Scan(&id)
	return id, err
}
