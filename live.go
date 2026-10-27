package main

import (
	"database/sql"
	"fmt"
	"html"
	"io"
	"strings"
	"time"
)

func (m *MediaHandlers) StreamKey(request *Request) *Response {
	account, failure := requireAccount(m.DB, request)
	if failure != nil {
		return failure
	}
	ctx, cancel := dbTimer()
	defer cancel()
	var key string
	if err := m.DB.QueryRowContext(ctx, "SELECT stream_key FROM users WHERE id=$1", account.DatabaseID).Scan(&key); err != nil {
		return mediaDBError(err)
	}
	return jsonResponse(map[string]string{"stream_key": key, "server": "rtmp://localhost:1935/live"})
}
func (m *MediaHandlers) liveHook(request *Request) (int64, map[string]string, *Response) {
	fields, err := ParseForm(request.Body)
	if err != nil || len(fields["name"]) != 64 {
		return 0, nil, ErrorResponse(403, "Invalid stream key")
	}
	ctx, cancel := dbTimer()
	defer cancel()
	var id int64
	err = m.DB.QueryRowContext(ctx, "SELECT id FROM users WHERE stream_key=$1", fields["name"]).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil, ErrorResponse(403, "Invalid stream key")
	}
	if err != nil {
		return 0, nil, mediaDBError(err)
	}
	return id, fields, nil
}
func (m *MediaHandlers) LiveStart(request *Request) *Response {
	user, fields, failure := m.liveHook(request)
	if failure != nil {
		return failure
	}
	id, err := RandomHex(16)
	if err != nil {
		return ErrorResponse(500, "Could not start stream")
	}
	ctx, cancel := dbTimer()
	defer cancel()
	_, err = m.DB.ExecContext(ctx, `INSERT INTO live_streams(user_id,public_id,client_id,active,started_at)
 VALUES($1,$2,$3,TRUE,CURRENT_TIMESTAMP)
 ON CONFLICT(user_id) DO UPDATE SET public_id=EXCLUDED.public_id,client_id=EXCLUDED.client_id,
 active=TRUE,started_at=CURRENT_TIMESTAMP`, user, id, fields["clientid"])
	if err != nil {
		return mediaDBError(err)
	}
	return authText("Stream accepted")
}
func (m *MediaHandlers) LiveEnd(request *Request) *Response {
	user, fields, failure := m.liveHook(request)
	if failure != nil {
		return failure
	}
	ctx, cancel := dbTimer()
	defer cancel()
	_, err := m.DB.ExecContext(ctx, "UPDATE live_streams SET active=FALSE WHERE user_id=$1 AND client_id=$2", user, fields["clientid"])
	if err != nil {
		return mediaDBError(err)
	}
	return authText("Stream ended")
}

type LiveInfo struct {
	ID       string `json:"id"`
	Author   string `json:"author"`
	ImageURL string `json:"imageURL"`
	Path     string `json:"hls_path"`
}

func (m *MediaHandlers) streamFresh(key string, started time.Time) bool {
	info, err := m.LiveRoot.Stat(key + "/index.m3u8")
	if err != nil {
		return time.Since(started) < 45*time.Second
	}
	return time.Since(info.ModTime()) < 30*time.Second || time.Since(started) < 45*time.Second
}
func (m *MediaHandlers) LiveList(request *Request) *Response {
	ctx, cancel := dbTimer()
	defer cancel()
	rows, err := m.DB.QueryContext(ctx, `SELECT s.public_id,u.display_name,u.image_url,u.stream_key,s.started_at
 FROM live_streams s JOIN users u ON u.id=s.user_id WHERE s.active=TRUE ORDER BY s.started_at DESC`)
	if err != nil {
		return mediaDBError(err)
	}
	defer rows.Close()
	streams := make([]LiveInfo, 0)
	for rows.Next() {
		var live LiveInfo
		var key string
		var started time.Time
		if err = rows.Scan(&live.ID, &live.Author, &live.ImageURL, &key, &started); err != nil {
			return mediaDBError(err)
		}
		if !m.streamFresh(key, started) {
			continue
		}
		live.Author = html.EscapeString(live.Author)
		live.ImageURL = html.EscapeString(live.ImageURL)
		live.Path = "/live-media/" + live.ID + "/index.m3u8"
		streams = append(streams, live)
	}
	if err = rows.Err(); err != nil {
		return mediaDBError(err)
	}
	return jsonResponse(map[string]any{"streams": streams})
}
func liveSegment(name string) bool {
	if !strings.HasSuffix(name, ".ts") {
		return false
	}
	digits := strings.TrimSuffix(name, ".ts")
	return digits != "" && strings.Trim(digits, "0123456789") == ""
}
func cleanLivePlaylist(body []byte, key string) ([]byte, error) {
	// Reject unexpected secret-bearing or URI-tag content instead of passing it through.
	if strings.Contains(string(body), key) {
		return nil, fmt.Errorf("private path in playlist")
	}
	var result strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			allowed := false
			for _, prefix := range []string{"#EXTM3U", "#EXT-X-VERSION:", "#EXT-X-TARGETDURATION:", "#EXT-X-MEDIA-SEQUENCE:", "#EXT-X-DISCONTINUITY", "#EXT-X-ENDLIST", "#EXTINF:", "#EXT-X-INDEPENDENT-SEGMENTS", "#EXT-X-PROGRAM-DATE-TIME:"} {
				if strings.HasPrefix(line, prefix) {
					allowed = true
					break
				}
			}
			if !allowed || strings.Contains(line, "URI=") {
				return nil, fmt.Errorf("unsupported live playlist tag")
			}
		} else if !liveSegment(line) {
			return nil, fmt.Errorf("invalid live segment")
		}
		result.WriteString(line)
		result.WriteByte('\n')
	}
	return []byte(result.String()), nil
}
func (m *MediaHandlers) LiveFile(request *Request) *Response {
	parts := strings.Split(strings.TrimPrefix(request.Path, "/live-media/"), "/")
	if len(parts) != 2 || !validID(parts[0]) || (parts[1] != "index.m3u8" && !liveSegment(parts[1])) {
		return ErrorResponse(404, "Not Found")
	}
	ctx, cancel := dbTimer()
	defer cancel()
	var key string
	var started time.Time
	err := m.DB.QueryRowContext(ctx, `SELECT u.stream_key,s.started_at FROM live_streams s JOIN users u ON u.id=s.user_id
 WHERE s.public_id=$1 AND s.active=TRUE`, parts[0]).Scan(&key, &started)
	if err == sql.ErrNoRows {
		return ErrorResponse(404, "Stream ended")
	}
	if err != nil {
		return mediaDBError(err)
	}
	if !m.streamFresh(key, started) {
		return ErrorResponse(404, "Stream ended")
	}
	name := key + "/" + parts[1]
	if parts[1] != "index.m3u8" {
		return ServeDiskFile(m.LiveRoot, name, request, "no-store")
	}
	file, err := m.LiveRoot.Open(name)
	if err != nil {
		return ErrorResponse(503, "Stream is starting")
	}
	defer file.Close()
	// This is a small local playlist, not request-body buffering.
	body, err := io.ReadAll(io.LimitReader(file, 128*1024+1))
	if err != nil || len(body) > 128*1024 {
		return ErrorResponse(503, "Playlist unavailable")
	}
	safe, err := cleanLivePlaylist(body, key)
	if err != nil {
		return ErrorResponse(503, "Playlist unavailable")
	}
	response := NewResponse()
	response.AddHeader("Cache-Control", "no-store")
	response.SetBinary(safe, "application/vnd.apple.mpegurl")
	return response
}
