package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func OpenDatabase() (*sql.DB, error) {
	// get database address and login from the terminal environment
	connectionString := os.Getenv("DATABASE_URL")
	if connectionString == "" {
		return nil, fmt.Errorf("set DATABASE_URL before starting the server")
	}

	db, err := sql.Open("pgx", connectionString)
	if err != nil {
		return nil, err
	}
	// limit database connections
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)

	// give the connection check 5s to finish
	timer, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// check database connection before starting the server
	if err := db.PingContext(timer); err != nil {
		db.Close()
		return nil, fmt.Errorf("could not connect to PostgreSQL: %w", err)
	}
	return db, nil
}

func CreateTables(db *sql.DB) error {
	// stop if database setup takes too long
	timer, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// create both tables together, or undo setup if either step fails
	tm, err := db.BeginTx(timer, nil)
	if err != nil {
		return err
	}
	defer tm.Rollback()

	// each person has name and secret token hash
	_, err = tm.ExecContext(timer, `
		CREATE TABLE IF NOT EXISTS guests (
			id BIGSERIAL PRIMARY KEY,
			author TEXT NOT NULL UNIQUE,
			token_hash TEXT NOT NULL UNIQUE
		)
	`)

	if err != nil {
		return fmt.Errorf("creating guests table: %w", err)
	}

	_, err = tm.ExecContext(timer, `
		CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			display_name TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("creating users table: %w", err)
	}

	// owner_id connects message to owner
	// updated starts false and becomes true when message is edited
	_, err = tm.ExecContext(timer, `
		CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			owner_id BIGINT NOT NULL REFERENCES guests(id),
			content TEXT NOT NULL,
			updated BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("creating messages table: %w", err)
	}

	// Add account ownership without removing existing guest messages.
	_, err = tm.ExecContext(timer, `
		ALTER TABLE users ADD COLUMN IF NOT EXISTS public_id TEXT;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS image_url TEXT NOT NULL DEFAULT '/public/imgs/user.webp';
		ALTER TABLE users DROP CONSTRAINT IF EXISTS users_password_hash_key;
		ALTER TABLE users DROP CONSTRAINT IF EXISTS users_display_name_key;
		ALTER TABLE messages ADD COLUMN IF NOT EXISTS user_id BIGINT REFERENCES users(id);
		CREATE TABLE IF NOT EXISTS auth_sessions (
			token_hash TEXT PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TIMESTAMPTZ NOT NULL
		);
		CREATE INDEX IF NOT EXISTS auth_sessions_expiry ON auth_sessions(expires_at);
	`)
	if err != nil {
		return fmt.Errorf("adding accounts: %w", err)
	}

	// Give older accounts random public IDs while retaining internal IDs.
	rows, err := tm.QueryContext(timer, "SELECT id FROM users WHERE public_id IS NULL")
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		publicID, err := RandomHex(16)
		if err != nil {
			return err
		}
		if _, err := tm.ExecContext(timer, "UPDATE users SET public_id=$1 WHERE id=$2", publicID, id); err != nil {
			return err
		}
	}
	_, err = tm.ExecContext(timer, `
		ALTER TABLE users ALTER COLUMN public_id SET NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS users_public_id_key ON users(public_id);
	`)
	if err != nil {
		return err
	}

	if err := CreateMediaTables(timer, tm); err != nil {
		return fmt.Errorf("creating media tables: %w", err)
	}
	// keep the tables, existing rows are left alone
	return tm.Commit()
}
