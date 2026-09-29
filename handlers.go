package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// root limits file access to the public folder
type FileHandlers struct {
	Root *os.Root
}

func CreateRouter(root *os.Root) *Router {
	// files holds access to public, router starts with no routes
	files := &FileHandlers{Root: root}
	router := &Router{}
	// false requires an exact path, true allows any path with this prefix
	router.AddRoute("GET", "/", false, files.Home)
	router.AddRoute("GET", "/chat", false, files.Chat)
	router.AddRoute("GET", "/public/", true, files.StaticFile)
	return router
}

func (files *FileHandlers) Home(request *Request) *Response {
	return files.RenderPage("index.html")
}

func (files *FileHandlers) Chat(request *Request) *Response {
	return files.RenderPage("chat.html")
}

func (files *FileHandlers) RenderPage(name string) *Response {
	// read the shared layout containing menus and the content placeholder
	layout, err := files.Root.ReadFile("layout/layout.html")
	if err != nil {
		// 500 means the server could not load a file it needs
		return ErrorResponse(500, "Internal Server Error")
	}
	// read the page we want to insert, like index.html or chat.html
	page, err := files.Root.ReadFile(name)
	if err != nil {
		return ErrorResponse(500, "Internal Server Error")
	}
	// put page content inside the layout
	html := strings.ReplaceAll(string(layout), "{{content}}", string(page))
	response := NewResponse()
	// tell the browser to render HTML and use UTF-8 for emojis
	response.SetBinary([]byte(html), "text/html; charset=utf-8")
	return response
}

func (files *FileHandlers) StaticFile(request *Request) *Response {
	// remove /public/ because root already points to public
	name := strings.TrimPrefix(request.Path, "/public/")
	// reject Windows path separators, drive/stream colons, and null bytes
	if name == "" || strings.ContainsAny(name, "\\:\x00") {
		return ErrorResponse(404, "Not Found")
	}
	// block attempts to leave the public folder
	for _, part := range strings.Split(name, "/") {
		// .. means parent folder, . means current folder
		if part == ".." || part == "." || part == "" {
			return ErrorResponse(404, "Not Found")
		}
	}
	// only serve regular files, not folders or special devices
	info, err := files.Root.Stat(name)
	// Stat gets file information without reading the file contents
	if err != nil || !info.Mode().IsRegular() {
		return ErrorResponse(404, "Not Found")
	}
	// read raw bytes so images are sent without changing their contents
	body, err := files.Root.ReadFile(name)
	if err != nil {
		return ErrorResponse(404, "Not Found")
	}
	response := NewResponse()
	// attach the bytes and the content type for this file
	response.SetBinary(body, FileContentType(name))
	// hash file contents so the tag changes when the file changes
	hash := sha256.Sum256(body)
	etag := fmt.Sprintf("\"%x\"", hash)
	response.AddHeader("ETag", etag)
	// browser can reuse the file for 10 seconds before checking again
	response.AddHeader("Cache-Control", "public, max-age=10, must-revalidate")
	if ETagMatches(request.Headers["if-none-match"], etag) {
		// browser already has this version, so send headers without the file
		response.SetStatus(304, "Not Modified")
		response.Body = nil
	}
	return response
}

func ETagMatches(header string, etag string) bool {
	// clients can send a list of tags or * for any existing version
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" {
			return true
		}
		// weak tags use W/ before the quoted value and also work for GET
		tag = strings.TrimPrefix(tag, "W/")
		if tag == etag {
			return true
		}
	}
	return false
}

// choose content type from file extension
func FileContentType(name string) string {
	// Ext gets the ending, like .jpg; lowercase also handles .JPG
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	default:
		// unknown extensions are treated as general binary data
		return "application/octet-stream"
	}
}
