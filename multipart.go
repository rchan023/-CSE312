package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

type MultipartPart struct {
	Name     string
	Filename string
	Headers  map[string]string
	Offset   int64
	Size     int64
	source   io.ReaderAt
}

func (p MultipartPart) Reader() *io.SectionReader {
	return io.NewSectionReader(p.source, p.Offset, p.Size)
}
func (p MultipartPart) Text(limit int64) (string, error) {
	if p.Size > limit {
		return "", fmt.Errorf("form text is too long")
	}
	data := make([]byte, int(p.Size))
	n, err := p.source.ReadAt(data, p.Offset)
	if err != nil && err != io.EOF {
		return "", err
	}
	if n != len(data) {
		return "", io.ErrUnexpectedEOF
	}
	return string(data), nil
}

// Parse only header parameters, never file contents.
func headerParameters(value string) (string, map[string]string, error) {
	var pieces []string
	start := 0
	quoted := false
	escaped := false
	for i, c := range value {
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if c == ';' && !quoted {
			pieces = append(pieces, value[start:i])
			start = i + 1
		}
	}
	if quoted || escaped {
		return "", nil, fmt.Errorf("invalid quoted parameter")
	}
	pieces = append(pieces, value[start:])
	params := map[string]string{}
	for _, piece := range pieces[1:] {
		pair := strings.SplitN(strings.TrimSpace(piece), "=", 2)
		if len(pair) != 2 {
			return "", nil, fmt.Errorf("invalid header parameter")
		}
		name := strings.ToLower(strings.TrimSpace(pair[0]))
		val := strings.TrimSpace(pair[1])
		if strings.HasPrefix(val, "\"") {
			if len(val) < 2 || val[len(val)-1] != '"' {
				return "", nil, fmt.Errorf("invalid parameter")
			}
			val = val[1 : len(val)-1]
			var decoded strings.Builder
			for i := 0; i < len(val); i++ {
				if val[i] == '\\' {
					i++
					if i == len(val) {
						return "", nil, fmt.Errorf("invalid escape")
					}
				}
				decoded.WriteByte(val[i])
			}
			val = decoded.String()
		}
		if _, exists := params[name]; exists {
			return "", nil, fmt.Errorf("duplicate parameter")
		}
		params[name] = val
	}
	return strings.ToLower(strings.TrimSpace(pieces[0])), params, nil
}

// Scan with overlap so a boundary split between buffers is still found.
func findBytes(source io.ReaderAt, start, end int64, needle []byte) (int64, error) {
	buffer := make([]byte, 64*1024+len(needle))
	for offset := start; offset < end; {
		count := int(min(int64(len(buffer)), end-offset))
		n, err := source.ReadAt(buffer[:count], offset)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if at := bytes.Index(buffer[:n], needle); at >= 0 {
			return offset + int64(at), nil
		}
		if n < count || offset+int64(n) >= end {
			break
		}
		advance := n - len(needle) + 1
		if advance <= 0 {
			break
		}
		offset += int64(advance)
	}
	return 0, fmt.Errorf("multipart delimiter not found")
}
func readAtBytes(source io.ReaderAt, offset int64, count int) ([]byte, error) {
	data := make([]byte, count)
	n, err := source.ReadAt(data, offset)
	if n != count {
		return nil, io.ErrUnexpectedEOF
	}
	if err != nil && err != io.EOF {
		return nil, err
	}
	return data, nil
}
func findPartEnd(source io.ReaderAt, start, total int64, marker []byte) (int64, bool, int64, error) {
	for start < total {
		at, err := findBytes(source, start, total, marker)
		if err != nil {
			return 0, false, 0, err
		}
		tail := at + int64(len(marker))
		suffix, err := readAtBytes(source, tail, 2)
		if err != nil {
			return 0, false, 0, err
		}
		if bytes.Equal(suffix, []byte("\r\n")) {
			return at, false, tail + 2, nil
		}
		if bytes.Equal(suffix, []byte("--")) {
			// A closing delimiter ends here or is followed by CRLF.
			if tail+2 == total {
				return at, true, total, nil
			}
			after, err := readAtBytes(source, tail+2, 2)
			if err == nil && bytes.Equal(after, []byte("\r\n")) {
				return at, true, tail + 4, nil
			}
		}
		start = at + 1
	}
	return 0, false, 0, fmt.Errorf("missing closing boundary")
}

func ParseMultipart(request *Request) (map[string]MultipartPart, error) {
	media, params, err := headerParameters(request.Headers["content-type"])
	if err != nil || media != "multipart/form-data" {
		return nil, fmt.Errorf("expected multipart form")
	}
	boundary := params["boundary"]
	if len(boundary) == 0 || len(boundary) > 200 || strings.ContainsAny(boundary, "\r\n\x00") {
		return nil, fmt.Errorf("invalid boundary")
	}
	source, total := request.Data()
	opening := []byte("--" + boundary + "\r\n")
	first, err := readAtBytes(source, 0, len(opening))
	if err != nil || !bytes.Equal(first, opening) {
		return nil, fmt.Errorf("missing opening boundary")
	}
	position := int64(len(opening))
	parts := map[string]MultipartPart{}
	marker := []byte("\r\n--" + boundary)
	for len(parts) < 32 {
		headerEnd, err := findBytes(source, position, min(total, position+16*1024), []byte("\r\n\r\n"))
		if err != nil {
			return nil, fmt.Errorf("invalid part headers")
		}
		headerBytes, err := readAtBytes(source, position, int(headerEnd-position))
		if err != nil {
			return nil, err
		}
		headers := map[string]string{}
		for _, line := range strings.Split(string(headerBytes), "\r\n") {
			pair := strings.SplitN(line, ":", 2)
			if len(pair) != 2 {
				return nil, fmt.Errorf("invalid part header")
			}
			name := strings.ToLower(strings.TrimSpace(pair[0]))
			if _, exists := headers[name]; exists {
				return nil, fmt.Errorf("duplicate part header")
			}
			headers[name] = strings.TrimSpace(pair[1])
		}
		disposition, attributes, err := headerParameters(headers["content-disposition"])
		if err != nil || disposition != "form-data" || attributes["name"] == "" {
			return nil, fmt.Errorf("invalid part disposition")
		}
		name := attributes["name"]
		if _, exists := parts[name]; exists {
			return nil, fmt.Errorf("duplicate part")
		}
		dataStart := headerEnd + 4
		dataEnd, closing, next, err := findPartEnd(source, dataStart, total, marker)
		if err != nil {
			return nil, err
		}
		parts[name] = MultipartPart{Name: name, Filename: attributes["filename"], Headers: headers, Offset: dataStart, Size: dataEnd - dataStart, source: source}
		if closing {
			return parts, nil
		}
		position = next
	}
	return nil, fmt.Errorf("too many multipart parts")
}
