package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf8"
)

type ChatHandlers struct {
	DB *sql.DB
}

// json names match what the frontend expects
type ChatMessage struct {
	Author   string `json:"author"`
	ID       string `json:"id"`
	Content  string `json:"content"`
	Updated  bool   `json:"updated"`
	ImageURL string `json:"imageURL"`
}

func AddChatRoutes(router *Router, db *sql.DB) {
	chat := &ChatHandlers{DB: db}
	router.AddRoute("POST", "/api/chats", false, chat.CreateMessage)
	router.AddRoute("GET", "/api/chats", false, chat.GetMessages)
	router.AddRoute("PATCH", "/api/chats/", true, chat.UpdateMessage)
	router.AddRoute("DELETE", "/api/chats/", true, chat.DeleteMessage)
}

// create random text for tokens and message ids
func RandomHex(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

// store the hash in PostgreSQL, not the secret cookie itself
func TokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// find our cookie among the browser's other cookies
func GuestToken(request *Request) string {
	for _, cookie := range strings.Split(request.Headers["cookie"], ";") {
		parts := strings.SplitN(strings.TrimSpace(cookie), "=", 2)
		if len(parts) == 2 && parts[0] == "guest_token" {
			// our token contains 32 random bytes written as 64 hex characters
			decoded, err := hex.DecodeString(parts[1])
			if err == nil && len(decoded) == 32 {
				return parts[1]
			}
		}
	}
	return ""
}

func (chat *ChatHandlers) CreateMessage(request *Request) *Response {
	// pointer lets us tell a missing content field from an empty string
	var input struct {
		Content *string `json:"content"`
	}
	if !utf8.Valid(request.Body) || json.Unmarshal(request.Body, &input) != nil || input.Content == nil {
		return ErrorResponse(400, "Bad Request")
	}
	// PostgreSQL text cannot contain a null byte
	if strings.ContainsRune(*input.Content, '\x00') {
		return ErrorResponse(400, "Bad Request")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// save a new guest and their first message together
	tx, err := chat.DB.BeginTx(ctx, nil)
	if err != nil {
		return ChatDatabaseError(err)
	}
	defer tx.Rollback()

	var ownerID int64
	token := GuestToken(request)
	newToken := ""
	if token != "" {
		// $1 passes data separately so it cannot become SQL commands
		err = tx.QueryRowContext(ctx, "SELECT id FROM guests WHERE token_hash = $1", TokenHash(token)).Scan(&ownerID)
		if err != nil && err != sql.ErrNoRows {
			return ChatDatabaseError(err)
		}
	}

	if ownerID == 0 {
		newToken, err = RandomHex(32)
		if err != nil {
			return ChatDatabaseError(err)
		}
		// the public name is random too, but never reveals the secret token
		nameID, err := RandomHex(16)
		if err != nil {
			return ChatDatabaseError(err)
		}
		err = tx.QueryRowContext(ctx,
			"INSERT INTO guests (author, token_hash) VALUES ($1, $2) RETURNING id",
			"Guest-"+nameID, TokenHash(newToken)).Scan(&ownerID)
		if err != nil {
			return ChatDatabaseError(err)
		}
	}

	messageID, err := RandomHex(16)
	if err != nil {
		return ChatDatabaseError(err)
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO messages (id, owner_id, content) VALUES ($1, $2, $3)",
		messageID, ownerID, *input.Content)
	if err != nil {
		return ChatDatabaseError(err)
	}
	if err = tx.Commit(); err != nil {
		return ChatDatabaseError(err)
	}

	response := NewResponse()
	response.SetText("Message sent")
	response.AddHeader("Cache-Control", "no-store")
	if newToken != "" {
		// keep the identity after closing the browser; scripts cannot read it
		response.AddHeader("Set-Cookie", "guest_token="+newToken+"; Path=/; HttpOnly; SameSite=Lax; Max-Age=31536000")
	}
	return response
}

func (chat *ChatHandlers) GetMessages(request *Request) *Response {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// join each message to its author's name
	rows, err := chat.DB.QueryContext(ctx, `
		SELECT guests.author, messages.id, messages.content, messages.updated
		FROM messages JOIN guests ON messages.owner_id = guests.id
		ORDER BY messages.created_at, messages.id
	`)
	if err != nil {
		return ChatDatabaseError(err)
	}
	defer rows.Close()

	// empty slice becomes [] in JSON instead of null
	messages := make([]ChatMessage, 0)
	for rows.Next() {
		var message ChatMessage
		if err := rows.Scan(&message.Author, &message.ID, &message.Content, &message.Updated); err != nil {
			return ChatDatabaseError(err)
		}
		// escape when sending, so HTML displays as text instead of running
		message.Content = html.EscapeString(message.Content)
		message.Author = html.EscapeString(message.Author)
		message.ImageURL = "/public/imgs/user.webp"
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return ChatDatabaseError(err)
	}
	response := NewResponse()
	if err := response.SetJSON(map[string]any{"messages": messages}); err != nil {
		return ChatDatabaseError(err)
	}
	response.AddHeader("Cache-Control", "no-store")
	return response
}

// get the message id from a path like /api/chats/abc123
func ChatMessageID(request *Request) string {
	id := strings.TrimPrefix(request.Path, "/api/chats/")
	// our ids are 16 random bytes written as 32 hex characters
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 {
		return ""
	}
	return id
}

func (chat *ChatHandlers) UpdateMessage(request *Request) *Response {
	id := ChatMessageID(request)
	if id == "" {
		return ErrorResponse(404, "Not Found")
	}
	token := GuestToken(request)
	if token == "" {
		return ErrorResponse(403, "Forbidden")
	}
	// check the new message text before sending it to PostgreSQL
	var input struct {
		Content *string `json:"content"`
	}
	if !utf8.Valid(request.Body) || json.Unmarshal(request.Body, &input) != nil || input.Content == nil {
		return ErrorResponse(400, "Bad Request")
	}
	if strings.ContainsRune(*input.Content, '\x00') {
		return ErrorResponse(400, "Bad Request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// update only if the cookie belongs to the message owner
	// checking ownership inside the query prevents a separate check becoming stale
	result, err := chat.DB.ExecContext(ctx, `
		UPDATE messages SET content = $1, updated = TRUE
		WHERE id = $2 AND owner_id = (
			SELECT id FROM guests WHERE token_hash = $3
		)
	`, *input.Content, id, TokenHash(token))
	if err != nil {
		return ChatDatabaseError(err)
	}
	return MessageChangeResponse(result, "Message updated")
}

func (chat *ChatHandlers) DeleteMessage(request *Request) *Response {
	id := ChatMessageID(request)
	if id == "" {
		return ErrorResponse(404, "Not Found")
	}
	token := GuestToken(request)
	if token == "" {
		return ErrorResponse(403, "Forbidden")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// delete only the message owned by this cookie's guest
	result, err := chat.DB.ExecContext(ctx, `
		DELETE FROM messages
		WHERE id = $1 AND owner_id = (
			SELECT id FROM guests WHERE token_hash = $2
		)
	`, id, TokenHash(token))
	if err != nil {
		return ChatDatabaseError(err)
	}
	return MessageChangeResponse(result, "Message deleted")
}

// check whether the update or delete actually changed a row
func MessageChangeResponse(result sql.Result, message string) *Response {
	count, err := result.RowsAffected()
	if err != nil {
		return ChatDatabaseError(err)
	}
	// no matching owned message, so nothing was changed
	if count == 0 {
		return ErrorResponse(403, "Forbidden")
	}
	response := NewResponse()
	response.SetText(message)
	response.AddHeader("Cache-Control", "no-store")
	return response
}

// show database errors in the terminal, not in the browser
func ChatDatabaseError(err error) *Response {
	fmt.Println("Chat error:", err)
	return ErrorResponse(500, "Internal Server Error")
}
