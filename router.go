package main

import (
	"net/url"
	"strings"
)

type Route struct {
	Method  string
	Path    string
	Prefix  bool                     // true means match paths starting with this path
	Handler func(*Request) *Response // takes a request and returns a response
}

// stores routes in order
type Router struct {
	Routes []Route
}

// add route to end of list
func (router *Router) AddRoute(method string, path string, prefix bool, handler func(*Request) *Response) {
	router.Routes = append(router.Routes, Route{method, path, prefix, handler})
}

func (router *Router) Route(request *Request) *Response {
	// remove query string, then decode things
	path := strings.SplitN(request.Path, "?", 2)[0]
	path, err := url.PathUnescape(path)
	// reject invalid paths, backslashes, and null bytes
	if err != nil || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00") {
		return ErrorResponse(404, "Not Found")
	}

	parsed := *request
	parsed.Path = path
	for _, route := range router.Routes {
		// skip if http method doesn't match request
		if route.Method != parsed.Method {
			continue
		}
		matches := parsed.Path == route.Path
		if route.Prefix {
			matches = strings.HasPrefix(parsed.Path, route.Path)
		}
		if matches {
			return route.Handler(&parsed)
		}
	}

	//no match
	return ErrorResponse(404, "Not Found")
}

func ErrorResponse(code int, message string) *Response {
	response := NewResponse()
	response.SetStatus(code, message)
	response.SetText(message)
	return response
}
