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

// store hash in database
func TokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// find cookie
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
	// pointer to compare empty vs. null
	var input struct {
		Content *string `json:"content"`
	}
	if !utf8.Valid(request.Body) || json.Unmarshal(request.Body, &input) != nil || input.Content == nil {
		return ErrorResponse(400, "Bad Request")
	}
	// SQl can't take NULL byte
	if strings.ContainsRune(*input.Content, '\x00') {
		return ErrorResponse(400, "Bad Request")
	}

	timer, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// save new person and their first message
	tx, err := chat.DB.BeginTx(timer, nil)
	if err != nil {
		return ChatDatabaseError(err)
	}
	defer tx.Rollback()

	var ownerID int64
	token := GuestToken(request)
	newToken := ""
	if token != "" {
		// lookup token hash / $1
		err = tx.QueryRowContext(timer, "SELECT id FROM guests WHERE token_hash = $1", TokenHash(token)).Scan(&ownerID)
		if err != nil && err != sql.ErrNoRows {
			return ChatDatabaseError(err)
		}
	}

	if ownerID == 0 {
		newToken, err = RandomHex(32)
		if err != nil {
			return ChatDatabaseError(err)
		}
		//random usernames
		nameID, err := RandomHex(16)
		if err != nil {
			return ChatDatabaseError(err)
		}
		err = tx.QueryRowContext(timer,
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
	_, err = tx.ExecContext(timer,
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
		// save identity even after closing browser
		response.AddHeader("Set-Cookie", "guest_token="+newToken+"; Path=/; HttpOnly; SameSite=Lax; Max-Age=31536000")
	}
	return response
}

func (chat *ChatHandlers) GetMessages(request *Request) *Response {
	// use a timer so the query doesn't run forever
	timer, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// add message to its sender
	rows, err := chat.DB.QueryContext(timer, `
		SELECT guests.author, messages.id, messages.content, messages.updated
		FROM messages JOIN guests ON messages.owner_id = guests.id
		ORDER BY messages.created_at, messages.id
	`)
	if err != nil {
		return ChatDatabaseError(err)
	}
	defer rows.Close()

	// empty slice becomes [] instead of null
	messages := make([]ChatMessage, 0)
	for rows.Next() {
		var message ChatMessage
		if err := rows.Scan(&message.Author, &message.ID, &message.Content, &message.Updated); err != nil {
			return ChatDatabaseError(err)
		}
		// HTML displays as text instead of executing scripts
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

// get the message id
func ChatMessageID(request *Request) string {
	id := strings.TrimPrefix(request.Path, "/api/chats/")
	// ids are 16 random bytes written as 32 hex characters
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
	// check new message before sending to SQL
	var input struct {
		Content *string `json:"content"`
	}
	if !utf8.Valid(request.Body) || json.Unmarshal(request.Body, &input) != nil || input.Content == nil {
		return ErrorResponse(400, "Bad Request")
	}
	if strings.ContainsRune(*input.Content, '\x00') {
		return ErrorResponse(400, "Bad Request")
	}
	timer, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// update only if the cookie belongs to the message owner
	result, err := chat.DB.ExecContext(timer, `
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
	timer, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// delete message if cookie belongs to owner
	result, err := chat.DB.ExecContext(timer, `
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

// check if update or delete changed row
func MessageChangeResponse(result sql.Result, message string) *Response {
	count, err := result.RowsAffected()
	if err != nil {
		return ChatDatabaseError(err)
	}
	// no matching owned message so nothing was changed
	if count == 0 {
		return ErrorResponse(403, "Forbidden")
	}
	response := NewResponse()
	response.SetText(message)
	response.AddHeader("Cache-Control", "no-store")
	return response
}

func ChatDatabaseError(err error) *Response {
	fmt.Println("Chat error:", err)
	return ErrorResponse(500, "Internal Server Error")
}
