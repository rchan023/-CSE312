package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func CreateMediaTables(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
  ALTER TABLE users ADD COLUMN IF NOT EXISTS stream_key TEXT;
  CREATE TABLE IF NOT EXISTS videos(
   id TEXT PRIMARY KEY,
   author_id BIGINT NOT NULL REFERENCES users(id),
   title TEXT NOT NULL,
   description TEXT NOT NULL,
   video_path TEXT NOT NULL,
   created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
   status TEXT NOT NULL DEFAULT 'queued',
   hls_path TEXT NOT NULL DEFAULT ''
  );
  CREATE TABLE IF NOT EXISTS live_streams(
   user_id BIGINT PRIMARY KEY REFERENCES users(id),
   public_id TEXT NOT NULL UNIQUE,
   client_id TEXT NOT NULL,
   active BOOLEAN NOT NULL DEFAULT TRUE,
   started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
 `)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM users WHERE stream_key IS NULL")
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
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
		key, err := RandomHex(32)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE users SET stream_key=$1 WHERE id=$2", key, id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE users ALTER COLUMN stream_key SET NOT NULL;
 CREATE UNIQUE INDEX IF NOT EXISTS users_stream_key_key ON users(stream_key);`)
	return err
}

func dbTimer() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
func mediaDBError(err error) *Response {
	// Do not print database parameters: some queries contain stream keys.
	fmt.Println("Media database operation failed")
	return ErrorResponse(500, "Could not complete media request")
}
