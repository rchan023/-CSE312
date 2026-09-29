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

	// keep the tables, existing rows are left alone
	return tm.Commit()
}
