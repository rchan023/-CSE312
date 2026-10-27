package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestFormAndValidators(t *testing.T) {
	fields, err := ParseForm([]byte("user%6Eame=Ryan&password=Hello%26%3D%2521%2B+123!"))
	if err != nil || fields["username"] != "Ryan" || fields["password"] != "Hello&=%21+ 123!" {
		t.Fatalf("decoded fields: %v, %v", fields, err)
	}
	for _, input := range []string{"username", "password=%", "password=%2", "password=%GG", "username=a&user%6Eame=b"} {
		if _, err := ParseForm([]byte(input)); err == nil {
			t.Errorf("accepted malformed form %q", input)
		}
	}
	for _, name := range []string{"", "a_b", "a-b", "a b", "é"} {
		if validUsername(name) {
			t.Errorf("accepted username %q", name)
		}
	}
	if !validUsername("Ryan123") {
		t.Fatal("rejected valid username")
	}
	for _, password := range []string{"Aa1!", "hello123!", "HELLO123!", "Helloabc!", "Hello1234", "Hello123?", "Hello123*", "Hello123 ", "Héllo123!"} {
		if validPassword(password) {
			t.Errorf("accepted invalid password %q", password)
		}
	}
	for _, char := range "!@#$%^&()-_=" {
		if !validPassword("Hello123" + string(char)) {
			t.Errorf("rejected allowed character %c", char)
		}
	}
}

func TestPasswordHashes(t *testing.T) {
	for _, password := range []string{"Hello123!", strings.Repeat("a", 80) + "A1!"} {
		first, err := hashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		second, err := hashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		if first == second || first == password {
			t.Fatal("password hashes must be salted")
		}
		if !checkPassword(first, password) || checkPassword(first, password+"x") {
			t.Fatal("password verification failed")
		}
	}
	hash, _ := hashPassword(strings.Repeat("a", 69) + "A1!")
	if checkPassword(hash, strings.Repeat("a", 69)+"A1!extra") {
		t.Fatal("long password was silently truncated")
	}
}

func TestMultipleCookies(t *testing.T) {
	response := NewResponse()
	response.AddHeader("Set-Cookie", "one=1; Path=/")
	response.AddHeader("set-cookie", "two=2; Path=/")
	raw := string(response.Bytes())
	if strings.Count(raw, "Set-Cookie:") != 2 {
		t.Fatal("lost a cookie")
	}
}

// Set RUN_DB_TESTS=1 in the compose server container. A temporary schema
// isolates these tests from the user's accounts and messages.
func TestDatabaseFoundation(t *testing.T) {
	if os.Getenv("RUN_DB_TESTS") != "1" {
		t.Skip("set RUN_DB_TESTS=1 with DATABASE_URL to run PostgreSQL integration tests")
	}
	config, err := pgx.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*config)
	defer admin.Close()
	suffix, _ := RandomHex(8)
	schema := "foundation_test_" + suffix
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	defer db.Close()
	db.SetMaxOpenConns(8)
	// Start with the old tables and a message to exercise migration.
	_, err = db.Exec(`CREATE TABLE guests(id BIGSERIAL PRIMARY KEY,author TEXT NOT NULL UNIQUE,token_hash TEXT NOT NULL UNIQUE);
 CREATE TABLE users(id BIGSERIAL PRIMARY KEY,username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL UNIQUE);
 CREATE TABLE messages(id TEXT PRIMARY KEY,owner_id BIGINT NOT NULL REFERENCES guests(id),content TEXT NOT NULL,updated BOOLEAN NOT NULL DEFAULT FALSE,created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP);
 INSERT INTO users(username,password_hash,display_name) VALUES ('OldAccount','not-a-real-hash','OldAccount');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = CreateTables(db); err != nil {
		t.Fatal(err)
	}
	if err = CreateTables(db); err != nil {
		t.Fatalf("migration is not repeatable: %v", err)
	}
	root, err := os.OpenRoot("public")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	router := CreateRouter(root)
	AddChatRoutes(router, db)
	AddAuthRoutes(router, db)
	call := func(method, path, body, cookie string) *Response {
		return router.Route(&Request{Method: method, Path: path, Version: "HTTP/1.1", Body: []byte(body), Headers: map[string]string{"content-type": "application/x-www-form-urlencoded", "cookie": cookie}})
	}
	require := func(response *Response, code int) {
		t.Helper()
		if response.StatusCode != code {
			t.Fatalf("want %d, got %d: %s", code, response.StatusCode, response.Body)
		}
	}
	cookie := func(response *Response, name string) string {
		for _, v := range response.Cookies {
			if strings.HasPrefix(v, name+"=") {
				return strings.SplitN(v, ";", 2)[0]
			}
		}
		t.Fatalf("missing %s cookie", name)
		return ""
	}
	getMessages := func() []ChatMessage {
		var result struct {
			Messages []ChatMessage `json:"messages"`
		}
		r := call("GET", "/api/chats", "", "")
		require(r, 200)
		if err := json.Unmarshal(r.Body, &result); err != nil {
			t.Fatal(err)
		}
		return result.Messages
	}
	require(call("GET", "/register", "", ""), 200)
	require(call("GET", "/login", "", ""), 200)
	require(call("GET", "/api/users/@me", "", ""), 401)
	require(call("POST", "/register", "username=bad_name&password=Hello123%21", ""), 400)
	require(call("POST", "/register", "username=Alice&password=Hello123%21", ""), 200)
	require(call("POST", "/register", "username=Alice&password=Hello123%21", ""), 400)
	require(call("POST", "/register", "username=Bob&password=Hello123%21", ""), 200)
	// Unique database constraint also handles simultaneous registration.
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- call("POST", "/register", "username=Concurrent&password=Hello123%21", "").StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	sum := 0
	for code := range statuses {
		sum += code
	}
	if sum != 600 {
		t.Fatalf("concurrent duplicate registration: %d", sum)
	}
	guestResponse := call("POST", "/api/chats", `{"content":"<script>guest</script>"}`, "")
	require(guestResponse, 200)
	guestCookie := cookie(guestResponse, "guest_token")
	messages := getMessages()
	if len(messages) != 1 || strings.Contains(messages[0].Content, "<script>") {
		t.Fatal("guest message escaping failed")
	}
	messageID := messages[0].ID
	require(call("POST", "/login", "username=Alice&password=Wrong123%21", guestCookie), 400)
	login := call("POST", "/login", "username=Alice&password=Hello123%21", guestCookie)
	require(login, 200)
	aliceCookie := cookie(login, "auth_token")
	if !strings.Contains(login.Cookies[0], "HttpOnly") || !strings.Contains(login.Cookies[0], "Max-Age=") {
		t.Fatal("missing auth cookie directives")
	}
	profile := call("GET", "/api/users/@me", "", aliceCookie)
	require(profile, 200)
	var account Account
	if err = json.Unmarshal(profile.Body, &account); err != nil {
		t.Fatal(err)
	}
	if len(account.ID) != 32 || account.Username != "Alice" || account.ImageURL != "/public/imgs/user.webp" {
		t.Fatalf("bad profile: %+v", account)
	}
	if strings.Contains(string(profile.Body), "hash") || strings.Contains(string(profile.Body), "token") {
		t.Fatal("profile exposes secret fields")
	}
	var storedHash string
	if err = db.QueryRow("SELECT token_hash FROM auth_sessions WHERE user_id=(SELECT id FROM users WHERE username='Alice')").Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	rawToken := strings.TrimPrefix(aliceCookie, "auth_token=")
	if storedHash != TokenHash(rawToken) || storedHash == rawToken {
		t.Fatal("session token not hashed")
	}
	if getMessages()[0].Author != "Alice" {
		t.Fatal("guest message not claimed")
	}
	require(call("PATCH", "/api/chats/"+messageID, `{"content":"owned"}`, aliceCookie), 200)
	require(call("DELETE", "/api/chats/"+messageID, "", guestCookie), 403)
	bobLogin := call("POST", "/login", "username=Bob&password=Hello123%21", guestCookie)
	require(bobLogin, 200)
	bobCookie := cookie(bobLogin, "auth_token")
	if getMessages()[0].Author != "Alice" {
		t.Fatal("second account reclaimed guest message")
	}
	require(call("PATCH", "/api/chats/"+messageID, `{"content":"stolen"}`, bobCookie), 403)
	require(call("DELETE", "/api/chats/"+messageID, "", bobCookie), 403)
	require(call("POST", "/api/chats", `{"content":"second"}`, aliceCookie), 200)
	// HW3 will change image_url on upload; all old messages use its current value.
	if _, err = db.Exec("UPDATE users SET image_url='/public/imgs/cat.jpg' WHERE username='Alice'"); err != nil {
		t.Fatal(err)
	}
	for _, message := range getMessages() {
		if message.Author == "Alice" && message.ImageURL != "/public/imgs/cat.jpg" {
			t.Fatal("stale avatar on old message")
		}
	}
	// New router represents restart: sessions are in PostgreSQL, not RAM.
	restarted := CreateRouter(root)
	AddAuthRoutes(restarted, db)
	require(restarted.Route(&Request{Method: "GET", Path: "/api/users/@me", Headers: map[string]string{"cookie": aliceCookie}}), 200)
	require(call("GET", "/logout", "", aliceCookie), 302)
	require(call("GET", "/api/users/@me", "", aliceCookie), 401)
	fresh := call("POST", "/login", "username=Alice&password=Hello123%21", "")
	require(fresh, 200)
	freshCookie := cookie(fresh, "auth_token")
	require(call("DELETE", "/api/chats/"+messageID, "", freshCookie), 200)
	if _, err = db.Exec("UPDATE auth_sessions SET expires_at=CURRENT_TIMESTAMP - INTERVAL '1 second'"); err != nil {
		t.Fatal(err)
	}
	require(call("GET", "/api/users/@me", "", freshCookie), 401)
	// Send two HTTP requests on one real connection to preserve keep-alive.
	server, client := net.Pipe()
	defer client.Close()
	go handleConnection(server, router)
	client.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(client)
	for _, path := range []string{"/login", "/api/users/@me"} {
		if _, err = fmt.Fprintf(client, "GET %s HTTP/1.1\r\nHost: localhost\r\n\r\n", path); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Fully consume the response so the next status line is aligned.
		var data [4096]byte
		for {
			_, err = response.Body.Read(data[:])
			if err != nil {
				break
			}
		}
		response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 401 {
			t.Fatal(response.StatusCode)
		}
	}
	// Ensure test DB remains reachable until all assertions have finished.
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}
