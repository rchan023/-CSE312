package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Response holds the information we want to send back to the browser.
type Response struct {
	KeepAlive  bool // true leaves the connection open for another request
	StatusCode int
	StatusText string
	Headers    map[string]string
	Body       []byte
}

// Start with a successful response and an empty header map.
func NewResponse() *Response {
	return &Response{
		StatusCode: 200,
		StatusText: "OK",
		Headers:    make(map[string]string),
	}
}

// A method is a function attached to a type. r is the Response being changed.
func (r *Response) SetStatus(code int, message string) {
	r.StatusCode = code
	r.StatusText = message
}

func (r *Response) AddHeader(name string, value string) {
	// Lowercase names prevent duplicate keys such as Content-Type/content-type.
	r.Headers[strings.ToLower(name)] = value
}

func (r *Response) SetText(text string) {
	r.AddHeader("Content-Type", "text/plain; charset=utf-8") //plain text
	r.Body = []byte(text)                                    //body of response
}

// Marshal converts Go data into JSON bytes. It does not handle HTTP for us.
func (r *Response) SetJSON(data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	r.AddHeader("Content-Type", "application/json")
	r.Body = body
	return nil
}

// Binary data stays as bytes, so images and other files are not changed.
func (r *Response) SetBinary(body []byte, contentType string) {
	r.AddHeader("Content-Type", contentType)
	r.Body = body
}

// Bytes builds the HTTP status line, headers, blank line, and body.
func (r *Response) Bytes() []byte {
	result := fmt.Sprintf("HTTP/1.1 %d %s\r\n", r.StatusCode, r.StatusText)
	// give the browser a starting time for cache freshness
	result += "Date: " + time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT") + "\r\n"

	for name, value := range r.Headers {
		// These three headers are always set below so they stay consistent.
		if name == "content-length" || name == "x-content-type-options" || name == "connection" {
			continue
		}
		result += name + ": " + value + "\r\n"
	}

	// Sprintf replaces %d with the length and %s with our message.
	connection := "Connection: close\r\n" //close connection after response
	if r.KeepAlive {
		connection = "Connection: keep-alive\r\n"
	}
	result += connection
	if r.StatusCode == 304 {
		// 304 has no body; leave out Content-Length rather than report zero
		result += "X-Content-Type-Options: nosniff\r\n\r\n"
		return []byte(result)
	}
	result += fmt.Sprintf(
		"Content-Length: %d\r\n"+ //length of body in bytes
			"X-Content-Type-Options: nosniff\r\n"+ //prevents browser from guessing content type
			"\r\n"+ //blank line separates headers and body
			"%s", //body of response
		len(r.Body), //length of body in bytes
		r.Body,      //body of response
	)
	return []byte(result)
}
