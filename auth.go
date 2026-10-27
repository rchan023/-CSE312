package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

type authHandlers struct{ DB *sql.DB }

// database ID stays internal; the browser gets the random public ID.
type Account struct {
	DatabaseID  int64  `json:"-"`
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	ImageURL    string `json:"imageURL"`
}

func AddAuthRoutes(router *Router, db *sql.DB) {
	auth := &authHandlers{DB: db}
	router.AddRoute("POST", "/register", false, auth.Register)
	router.AddRoute("POST", "/login", false, auth.Login)
	router.AddRoute("GET", "/logout", false, auth.Logout)
	router.AddRoute("GET", "/api/users/@me", false, auth.Me)
}

func ParseForm(body []byte) (map[string]string, error) {
	fields := make(map[string]string)
	pairs := strings.Split(string(body), "&")
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("form field is missing =")
		}
		key, err := decodeForm(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid form key: %w", err)
		}
		value, err := decodeForm(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid form value: %w", err)
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate form field")
		}
		fields[key] = value
	}
	return fields, nil
}

func ValueToHex(char byte) (byte, error) {
	if char >= '0' && char <= '9' {
		return char - '0', nil
	} else if char >= 'A' && char <= 'F' {
		return char - 'A' + 10, nil
	} else if char >= 'a' && char <= 'f' {
		return char - 'a' + 10, nil
	}
	return 0, fmt.Errorf("invalid hex character")
}

func decodeForm(value string) (string, error) {
	// replace + with space
	value = strings.ReplaceAll(value, "+", " ")
	result := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '%' {
			if i+2 >= len(value) {
				return "", fmt.Errorf("incomplete hex sequence at position %d", i)
			}
			hex1, err1 := ValueToHex(value[i+1])
			hex2, err2 := ValueToHex(value[i+2])
			if err1 != nil || err2 != nil {
				return "", fmt.Errorf("invalid hex sequence at position %d", i)
			}
			result = append(result, hex1<<4|hex2)
			i += 2
		} else {
			result = append(result, value[i])
		}
	}
	return string(result), nil
}

func validUsername(username string) bool {
	if len(username) == 0 {
		return false
	}
	for _, char := range username {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')) {
			return false
		}
	}
	return true
}

func validPassword(password string) bool {
	if len(password) < 8 {
		return false
	}
	islower, isupper, isdigit, isspecial := false, false, false, false
	for _, char := range password {
		if char >= 'a' && char <= 'z' {
			islower = true
		} else if char >= 'A' && char <= 'Z' {
			isupper = true
		} else if char >= '0' && char <= '9' {
			isdigit = true
		} else if strings.ContainsRune("!@#$%^&()-_=", char) {
			isspecial = true
		} else {
			return false
		}
	}
	return islower && isupper && isdigit && isspecial
}

// Bcrypt accepts at most 72 bytes, longer passwords use a marked prehash
// so never shorten the password
const longPasswordPrefix = "bcrypt-sha256:"

func hashPassword(password string) (string, error) {
	prefix := ""
	if len(password) > 72 {
		digest := sha256.Sum256([]byte(password))
		password = hex.EncodeToString(digest[:])
		prefix = longPasswordPrefix
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("error hashing password: %w", err)
	}
	return prefix + string(hash), nil
}

func checkPassword(hash, password string) bool {
	if strings.HasPrefix(hash, longPasswordPrefix) {
		digest := sha256.Sum256([]byte(password))
		password = hex.EncodeToString(digest[:])
		hash = strings.TrimPrefix(hash, longPasswordPrefix)
	} else if len(password) > 72 {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func formCredentials(request *Request) (map[string]string, *Response) {
	mediaType := strings.TrimSpace(strings.SplitN(request.Headers["content-type"], ";", 2)[0])
	if !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") {
		return nil, ErrorResponse(400, "Expected form data")
	}
	fields, err := ParseForm(request.Body)
	if err != nil {
		return nil, ErrorResponse(400, "Invalid form data")
	}
	return fields, nil
}

func (handlers *authHandlers) Register(request *Request) *Response {
	fields, failure := formCredentials(request)
	if failure != nil {
		return failure
	}
	username, password := fields["username"], fields["password"]
	if !validUsername(username) {
		return ErrorResponse(400, "Invalid username")
	}
	if !validPassword(password) {
		return ErrorResponse(400, "Invalid password")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return ErrorResponse(500, "Could not create account")
	}
	id, err := RandomHex(16)
	if err != nil {
		return ErrorResponse(500, "Could not create account")
	}
	streamKey, err := RandomHex(32)
	if err != nil {
		return ErrorResponse(500, "Could not create account")
	}
	timer, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = handlers.DB.ExecContext(timer,
		`INSERT INTO users (public_id, username, password_hash, display_name, stream_key) VALUES ($1,$2,$3,$2,$4)`, id, username, hash, streamKey)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return ErrorResponse(400, "Username is already taken")
		}
		return ErrorResponse(500, "Could not create account")
	}
	return authText("Account created")
}

func authText(message string) *Response {
	response := NewResponse()
	response.SetText(message)
	response.AddHeader("Cache-Control", "no-store")
	return response
}

func cookieToken(request *Request, name string) string {
	for _, item := range strings.Split(request.Headers["cookie"], ";") {
		parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(parts) == 2 && parts[0] == name {
			raw, err := hex.DecodeString(parts[1])
			if err == nil && len(raw) == 32 {
				return parts[1]
			}
			return ""
		}
	}
	return ""
}

// nil means the browser is a guest. Database errors are not guest logins.
func CurrentUser(db *sql.DB, request *Request) (*Account, error) {
	token := cookieToken(request, "auth_token")
	if token == "" {
		return nil, nil
	}
	timer, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	account := &Account{}
	err := db.QueryRowContext(timer, `
  SELECT u.id, u.public_id, u.username, u.display_name, u.image_url
  FROM users u JOIN auth_sessions s ON s.user_id = u.id
  WHERE s.token_hash = $1 AND s.expires_at > CURRENT_TIMESTAMP`, TokenHash(token)).Scan(
		&account.DatabaseID, &account.ID, &account.Username, &account.DisplayName, &account.ImageURL)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return account, nil
}

func (handlers *authHandlers) Login(request *Request) *Response {
	fields, failure := formCredentials(request)
	if failure != nil {
		return failure
	}
	username, password := fields["username"], fields["password"]
	if !validUsername(username) || !validPassword(password) {
		return ErrorResponse(400, "Invalid username or password")
	}
	timer, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var userID int64
	var hash string
	err := handlers.DB.QueryRowContext(timer, `SELECT id, password_hash FROM users WHERE username=$1`, username).Scan(&userID, &hash)
	if err == sql.ErrNoRows {
		return ErrorResponse(400, "Invalid username or password")
	}
	if err != nil {
		return ErrorResponse(500, "Could not log in")
	}
	if !checkPassword(hash, password) {
		return ErrorResponse(400, "Invalid username or password")
	}
	token, err := RandomHex(32)
	if err != nil {
		return ErrorResponse(500, "Could not log in")
	}
	tx, err := handlers.DB.BeginTx(timer, nil)
	if err != nil {
		return ErrorResponse(500, "Could not log in")
	}
	defer tx.Rollback()
	// Rotate the current browser's session and claim only unclaimed messages.
	_, err = tx.ExecContext(timer, `DELETE FROM auth_sessions WHERE token_hash=$1 OR expires_at <= CURRENT_TIMESTAMP`, TokenHash(cookieToken(request, "auth_token")))
	if err != nil {
		return ErrorResponse(500, "Could not log in")
	}
	_, err = tx.ExecContext(timer, `INSERT INTO auth_sessions (token_hash,user_id,expires_at) VALUES ($1,$2,$3)`, TokenHash(token), userID, time.Now().Add(30*24*time.Hour))
	if err != nil {
		return ErrorResponse(500, "Could not log in")
	}
	if guest := GuestToken(request); guest != "" {
		_, err = tx.ExecContext(timer, `UPDATE messages SET user_id=$1 WHERE user_id IS NULL AND owner_id=(SELECT id FROM guests WHERE token_hash=$2)`, userID, TokenHash(guest))
		if err != nil {
			return ErrorResponse(500, "Could not log in")
		}
	}
	if err := tx.Commit(); err != nil {
		return ErrorResponse(500, "Could not log in")
	}
	response := authText("Logged in")
	response.AddHeader("Set-Cookie", "auth_token="+token+"; Path=/; HttpOnly; SameSite=Lax; Max-Age=2592000")
	return response
}

func (handlers *authHandlers) Logout(request *Request) *Response {
	timer, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := handlers.DB.ExecContext(timer, `DELETE FROM auth_sessions WHERE token_hash=$1`, TokenHash(cookieToken(request, "auth_token")))
	if err != nil {
		return ErrorResponse(500, "Could not log out")
	}
	response := authText("Logged out")
	response.SetStatus(302, "Found")
	response.AddHeader("Location", "/")
	response.AddHeader("Set-Cookie", "auth_token=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0")
	return response
}

func (handlers *authHandlers) Me(request *Request) *Response {
	account, err := CurrentUser(handlers.DB, request)
	if err != nil {
		return ErrorResponse(500, "Could not load account")
	}
	response := NewResponse()
	response.AddHeader("Cache-Control", "no-store")
	if account == nil {
		response.SetStatus(401, "Unauthorized")
		_ = response.SetJSON(map[string]any{})
	} else {
		_ = response.SetJSON(account)
	}
	return response
}
