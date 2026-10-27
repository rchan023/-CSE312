package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Byte ranges include both endpoints.
func ParseByteRange(value string, size int64) (int64, int64, error) {
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") || size <= 0 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	pair := strings.SplitN(strings.TrimPrefix(value, "bytes="), "-", 2)
	if len(pair) != 2 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	number := func(s string) (int64, error) {
		if s == "" || strings.Trim(s, "0123456789") != "" {
			return 0, fmt.Errorf("invalid byte position")
		}
		return strconv.ParseInt(s, 10, 64)
	}
	if pair[0] == "" {
		n, err := number(pair[1])
		if err != nil || n <= 0 {
			return 0, 0, fmt.Errorf("invalid suffix")
		}
		return max(int64(0), size-n), size - 1, nil
	}
	first, err := number(pair[0])
	if err != nil || first >= size {
		return 0, 0, fmt.Errorf("range outside file")
	}
	last := size - 1
	if pair[1] != "" {
		last, err = number(pair[1])
		if err != nil || last < first {
			return 0, 0, fmt.Errorf("invalid end")
		}
		last = min(last, size-1)
	}
	return first, last, nil
}

type sectionCloser struct {
	io.Reader
	io.Closer
}

func safeRelative(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func ServeDiskFile(root *os.Root, name string, request *Request, cache string) *Response {
	if !safeRelative(name) {
		return ErrorResponse(404, "Not Found")
	}
	file, err := root.Open(name)
	if err != nil {
		return ErrorResponse(404, "Not Found")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return ErrorResponse(404, "Not Found")
	}
	response := NewResponse()
	response.AddHeader("Content-Type", FileContentType(name))
	response.AddHeader("Cache-Control", cache)
	size := info.Size()
	start := int64(0)
	length := size
	isMP4 := strings.HasSuffix(strings.ToLower(name), ".mp4")
	if isMP4 {
		response.AddHeader("Accept-Ranges", "bytes")
		if value, exists := request.Headers["range"]; exists {
			first, last, err := ParseByteRange(value, size)
			if err != nil {
				file.Close()
				response.SetStatus(416, "Range Not Satisfiable")
				response.AddHeader("Content-Range", fmt.Sprintf("bytes */%d", size))
				return response
			}
			start = first
			length = last - first + 1
			response.SetStatus(206, "Partial Content")
			response.AddHeader("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, size))
		}
	}
	response.Stream = &sectionCloser{Reader: io.NewSectionReader(file, start, length), Closer: file}
	response.StreamLength = length
	return response
}
