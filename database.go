package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	// register the PostgreSQL driver with database/sql
	_ "github.com/jackc/pgx/v5/stdlib"
)

func OpenDatabase() (*sql.DB, error) {
	// get database address and login from the terminal environment
	connectionString := os.Getenv("DATABASE_URL")
	if connectionString == "" {
		return nil, fmt.Errorf("set DATABASE_URL before starting the server")
	}

	// prepare connections using the pgx database driver
	db, err := sql.Open("pgx", connectionString)
	if err != nil {
		return nil, err
	}
	// limit how many database connections the server can open
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)

	// give the connection check 5 seconds to finish
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Open prepares the connection, Ping checks that the database answers
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("could not connect to PostgreSQL: %w", err)
	}
	return db, nil
}

func CreateTables(db *sql.DB) error {
	// stop if database setup takes too long
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// create both tables together, or undo setup if either step fails
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// each guest has a name and a hash of their secret cookie token
	// BIGSERIAL gives each new guest a different number
	_, err = tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS guests (
			id BIGSERIAL PRIMARY KEY,
			author TEXT NOT NULL UNIQUE,
			token_hash TEXT NOT NULL UNIQUE
		)
	`)
	if err != nil {
		return fmt.Errorf("creating guests table: %w", err)
	}

	// owner_id links each message to its guest
	// updated starts false and will become true when a message is edited
	_, err = tx.ExecContext(ctx, `
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

	// keep the tables, existing rows are left alone
	return tx.Commit()
}
