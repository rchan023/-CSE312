package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type MediaHandlers struct {
	DB        *sql.DB
	Directory string
	Root      *os.Root
	LiveRoot  *os.Root
}

func NewMediaHandlers(db *sql.DB, dir, live string) (*MediaHandlers, error) {
	for _, path := range []string{dir, live, filepath.Join(dir, "avatars"), filepath.Join(dir, "videos")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	liveRoot, err := os.OpenRoot(live)
	if err != nil {
		root.Close()
		return nil, err
	}
	return &MediaHandlers{DB: db, Directory: dir, Root: root, LiveRoot: liveRoot}, nil
}
func (m *MediaHandlers) Close() { m.Root.Close(); m.LiveRoot.Close() }
func (m *MediaHandlers) AddRoutes(router *Router) {
	router.AddRoute("POST", "/api/users/avatar", false, m.UploadAvatar)
	router.AddRoute("POST", "/api/videos", false, m.UploadVideo)
	router.AddRoute("GET", "/api/videos", false, m.Videos)
	router.AddRoute("GET", "/api/videos/", true, m.Video)
	router.AddRoute("GET", "/media/", true, m.File)
	router.AddRoute("GET", "/api/live/key", false, m.StreamKey)
	router.AddRoute("GET", "/api/live", false, m.LiveList)
	router.AddRoute("POST", "/api/live/start", false, m.LiveStart)
	router.AddRoute("POST", "/api/live/end", false, m.LiveEnd)
	router.AddRoute("GET", "/live-media/", true, m.LiveFile)
}
func validID(id string) bool {
	raw, err := hex.DecodeString(id)
	return err == nil && len(raw) == 16 && strings.ToLower(id) == id
}
func requireAccount(db *sql.DB, request *Request) (*Account, *Response) {
	account, err := CurrentUser(db, request)
	if err != nil {
		return nil, mediaDBError(err)
	}
	if account == nil {
		return nil, ErrorResponse(401, "Log in first")
	}
	return account, nil
}
func jsonResponse(data any) *Response {
	response := NewResponse()
	response.AddHeader("Cache-Control", "no-store")
	if err := response.SetJSON(data); err != nil {
		return ErrorResponse(500, "Could not encode response")
	}
	return response
}
func (m *MediaHandlers) File(request *Request) *Response {
	name := strings.TrimPrefix(request.Path, "/media/")
	// Only generated media locations are public; no arbitrary private files.
	segments := strings.Split(name, "/")
	if len(segments) < 2 || (segments[0] != "avatars" && segments[0] != "videos") {
		return ErrorResponse(404, "Not Found")
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".png", ".gif", ".mp4", ".m3u8", ".ts":
	default:
		return ErrorResponse(404, "Not Found")
	}
	return ServeDiskFile(m.Root, name, request, "no-cache")
}
func savePart(part MultipartPart, path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, part.Reader())
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}
func imageExtension(part MultipartPart) (string, error) {
	count := int(min(part.Size, 16))
	header, err := readAtBytes(part.source, part.Offset, count)
	if err != nil {
		return "", err
	}
	switch {
	case bytes.HasPrefix(header, []byte{0x89, 'P', 'N', 'G', 13, 10, 26, 10}):
		return ".png", nil
	case bytes.HasPrefix(header, []byte{0xff, 0xd8, 0xff}):
		return ".jpg", nil
	case bytes.HasPrefix(header, []byte("GIF87a")), bytes.HasPrefix(header, []byte("GIF89a")):
		return ".gif", nil
	}
	return "", fmt.Errorf("only JPEG, PNG, and GIF images are allowed")
}
func (m *MediaHandlers) UploadAvatar(request *Request) *Response {
	account, failure := requireAccount(m.DB, request)
	if failure != nil {
		return failure
	}
	parts, err := ParseMultipart(request)
	if err != nil {
		return ErrorResponse(400, "Invalid multipart upload")
	}
	avatar, ok := parts["avatar"]
	if !ok || avatar.Size == 0 {
		return ErrorResponse(400, "Missing avatar")
	}
	extension, err := imageExtension(avatar)
	if err != nil {
		return ErrorResponse(400, "Only JPEG, PNG, and GIF images are allowed")
	}
	id, err := RandomHex(16)
	if err != nil {
		return ErrorResponse(500, "Could not save avatar")
	}
	name := "avatars/" + id + extension
	path := filepath.Join(m.Directory, filepath.FromSlash(name))
	if err = savePart(avatar, path); err != nil {
		return ErrorResponse(500, "Could not save avatar")
	}
	ctx, cancel := dbTimer()
	defer cancel()
	_, err = m.DB.ExecContext(ctx, "UPDATE users SET image_url=$1 WHERE id=$2", "/media/"+name, account.DatabaseID)
	if err != nil {
		os.Remove(path)
		return mediaDBError(err)
	}
	return authText("Avatar updated")
}

type VideoInfo struct {
	ID           string `json:"id"`
	AuthorID     string `json:"author_id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	VideoPath    string `json:"video_path"`
	CreatedAt    string `json:"created_at"`
	ThumbnailURL string `json:"thumbnailURL"`
	Status       string `json:"status"`
	HLSPath      string `json:"hls_path,omitempty"`
}

func (m *MediaHandlers) UploadVideo(request *Request) *Response {
	account, failure := requireAccount(m.DB, request)
	if failure != nil {
		return failure
	}
	parts, err := ParseMultipart(request)
	if err != nil {
		return ErrorResponse(400, "Invalid multipart upload")
	}
	titlePart, tok := parts["title"]
	descriptionPart, dok := parts["description"]
	video, vok := parts["video"]
	if !tok || !dok || !vok || video.Size == 0 {
		return ErrorResponse(400, "Missing video fields")
	}
	title, err := titlePart.Text(1024)
	if err != nil {
		return ErrorResponse(400, "Title is too long")
	}
	description, err := descriptionPart.Text(16384)
	if err != nil {
		return ErrorResponse(400, "Description is too long")
	}
	if strings.TrimSpace(title) == "" || !utf8.ValidString(title) || !utf8.ValidString(description) || strings.ContainsRune(title+description, 0) {
		return ErrorResponse(400, "Invalid title or description")
	}
	id, err := RandomHex(16)
	if err != nil {
		return ErrorResponse(500, "Could not save video")
	}
	directory := filepath.Join(m.Directory, "videos", id)
	if err = os.MkdirAll(directory, 0755); err != nil {
		return ErrorResponse(500, "Could not save video")
	}
	path := filepath.Join(directory, "original.mp4")
	if err = savePart(video, path); err != nil {
		os.Remove(directory)
		return ErrorResponse(500, "Could not save video")
	}
	ctx, cancel := dbTimer()
	defer cancel()
	_, err = m.DB.ExecContext(ctx, `INSERT INTO videos(id,author_id,title,description,video_path) VALUES($1,$2,$3,$4,$5)`, id, account.DatabaseID, title, description, "/media/videos/"+id+"/original.mp4")
	if err != nil {
		os.Remove(path)
		os.Remove(directory)
		return mediaDBError(err)
	}
	// Workers independently claim queued rows. This request never waits for ffmpeg.
	return jsonResponse(map[string]string{"id": id})
}

const videoSelect = `SELECT v.id,u.public_id,v.title,v.description,v.video_path,v.created_at,v.status,v.hls_path
 FROM videos v JOIN users u ON v.author_id=u.id`

type rowScanner interface{ Scan(...any) error }

func scanVideo(row rowScanner) (VideoInfo, error) {
	var video VideoInfo
	var created time.Time
	err := row.Scan(&video.ID, &video.AuthorID, &video.Title, &video.Description, &video.VideoPath, &created, &video.Status, &video.HLSPath)
	video.Title = html.EscapeString(video.Title)
	video.Description = html.EscapeString(video.Description)
	video.CreatedAt = created.UTC().Format(time.RFC3339)
	video.ThumbnailURL = "/public/imgs/cat.jpg"
	return video, err
}
func (m *MediaHandlers) Videos(request *Request) *Response {
	ctx, cancel := dbTimer()
	defer cancel()
	rows, err := m.DB.QueryContext(ctx, videoSelect+" ORDER BY v.created_at DESC,v.id")
	if err != nil {
		return mediaDBError(err)
	}
	defer rows.Close()
	videos := make([]VideoInfo, 0)
	for rows.Next() {
		video, err := scanVideo(rows)
		if err != nil {
			return mediaDBError(err)
		}
		videos = append(videos, video)
	}
	if err = rows.Err(); err != nil {
		return mediaDBError(err)
	}
	return jsonResponse(map[string]any{"videos": videos})
}
func (m *MediaHandlers) Video(request *Request) *Response {
	id := strings.TrimPrefix(request.Path, "/api/videos/")
	if !validID(id) {
		return ErrorResponse(404, "Video not found")
	}
	ctx, cancel := dbTimer()
	defer cancel()
	video, err := scanVideo(m.DB.QueryRowContext(ctx, videoSelect+" WHERE v.id=$1", id))
	if err == sql.ErrNoRows {
		return ErrorResponse(404, "Video not found")
	}
	if err != nil {
		return mediaDBError(err)
	}
	return jsonResponse(map[string]any{"video": video})
}

// Used by workers as well as integration tests.
func (m *MediaHandlers) resumeConversions(ctx context.Context) error {
	_, err := m.DB.ExecContext(ctx, "UPDATE videos SET status='queued',hls_path='' WHERE status='processing'")
	return err
}
