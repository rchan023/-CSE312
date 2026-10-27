package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// collects one complete request from connection
func ReadRequest(source io.Reader) (*Request, error) {
	// bufio makes reading incoming bytes good
	reader, buffered := source.(*bufio.Reader)
	if !buffered {
		reader = bufio.NewReader(source)
	}
	headers := make([]byte, 0, 1024)

	// Stop only when end of the headers finishes
	for !bytes.HasSuffix(headers, []byte("\r\n\r\n")) {
		// limit header size
		if len(headers) >= 64*1024 {
			return nil, fmt.Errorf("request headers are too large")
		}
		//read next byte
		b, err := reader.ReadByte()
		if err != nil {
			if err == io.EOF && len(headers) > 0 {
				err = io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("reading request headers: %w", err)
		}
		headers = append(headers, b)
	}

	// find Content-Length
	request, err := ParseRequest(headers)
	if err != nil {
		return nil, err
	}
	if _, exists := request.Headers["transfer-encoding"]; exists {
		return nil, fmt.Errorf("transfer encoding is not supported yet")
	}

	// Read exactly Content-Length bytes; never consume the next request.
	var length int64
	if value, exists := request.Headers["content-length"]; exists {
		if value == "" || strings.Trim(value, "0123456789") != "" {
			return nil, fmt.Errorf("invalid Content-Length")
		}
		length, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid Content-Length")
		}
	}
	request.BodySize = length
	if length <= 1024*1024 {
		request.Body = make([]byte, 0, int(length))
	} else {
		request.BodyFile, err = os.CreateTemp("", "cse312-upload-*")
		if err != nil {
			return nil, err
		}
	}
	success := false
	defer func() {
		if !success {
			request.Cleanup()
		}
	}()
	buffer := make([]byte, 32*1024)
	for received := int64(0); received < length; {
		want := min(int64(len(buffer)), length-received)
		n, readErr := reader.Read(buffer[:int(want)])
		if n > 0 {
			if request.BodyFile != nil {
				written, writeErr := request.BodyFile.Write(buffer[:n])
				if writeErr != nil {
					return nil, writeErr
				}
				if written != n {
					return nil, io.ErrShortWrite
				}
			} else {
				request.Body = append(request.Body, buffer[:n]...)
			}
			received += int64(n)
		}
		if received == length {
			break
		}
		if readErr != nil {
			if readErr == io.EOF {
				readErr = io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("reading request body: %w", readErr)
		}
		if n == 0 {
			return nil, io.ErrNoProgress
		}
	}
	success = true
	return request, nil
}

type Request struct {
	BodyFile *os.File // large bodies stay on disk
	BodySize int64
	Version  string            // HTTP/1.1 or HTTP/1.0
	Method   string            //action such as GET, POST, etc
	Path     string            //target such as /chat
	Headers  map[string]string //holds metadata, make creates an empty header map
	Body     []byte            //holds data as bytes
}

// converts bytes into request
// *Request= a pointer to result
func ParseRequest(raw []byte) (*Request, error) {
	parts := bytes.SplitN(raw, []byte("\r\n\r\n"), 2) //splits the request into headers and body, limit 2 results
	if len(parts) != 2 {                              //error if not 2 parts
		return nil, fmt.Errorf("request headers are incomplete")
	}

	lines := strings.Split(string(parts[0]), "\r\n") // Convert only headers to text, leaving body as bytes
	requestLine := strings.Fields(lines[0])          //"GET /chat HTTP/1.1" becomes three separate fields
	if len(requestLine) != 3 {
		return nil, fmt.Errorf("invalid request line")
	}
	if requestLine[2] != "HTTP/1.1" && requestLine[2] != "HTTP/1.0" {
		return nil, fmt.Errorf("unsupported HTTP version")
	}

	// creates pointer to Request
	request := &Request{
		Version: requestLine[2],
		Method:  requestLine[0],
		Path:    requestLine[1],
		Headers: make(map[string]string),
		Body:    parts[1],
	}

	// Skip the request line and process remaining header line
	for _, line := range lines[1:] {
		// Header values can contain colons, so split only at the first one
		header := strings.SplitN(line, ":", 2)
		if len(header) != 2 || strings.TrimSpace(header[0]) == "" {
			return nil, fmt.Errorf("invalid header: %q", line)
		}
		// Store as lowercase
		name := strings.ToLower(strings.TrimSpace(header[0]))
		// Reject repeated headers instead of guessing body length
		if _, exists := request.Headers[name]; exists && (name == "content-length" || name == "transfer-encoding") {
			return nil, fmt.Errorf("duplicate  header: %s", name)
		}
		// remove space around the value
		if name == "connection" && request.Headers[name] != "" {
			request.Headers[name] += ", " + strings.TrimSpace(header[1])
			continue
		}
		request.Headers[name] = strings.TrimSpace(header[1])
	}

	return request, nil
}

func (request *Request) KeepAlive() bool {
	// HTTP/1.1 stays open by default, HTTP/1.0 closes by default
	keepAlive := request.Version == "HTTP/1.1"
	for _, value := range strings.Split(request.Headers["connection"], ",") {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "close":
			return false
		case "keep-alive":
			keepAlive = true
		}
	}
	return keepAlive
}

func (request *Request) Cleanup() {
	if request.BodyFile != nil {
		name := request.BodyFile.Name()
		request.BodyFile.Close()
		os.Remove(name)
		request.BodyFile = nil
	}
}
func (request *Request) Data() (io.ReaderAt, int64) {
	if request.BodyFile != nil {
		return request.BodyFile, request.BodySize
	}
	return bytes.NewReader(request.Body), int64(len(request.Body))
}
