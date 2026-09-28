package main

import (
	"net/url"
	"strings"
)

// one rule connecting a method and path to a function
type Route struct {
	Method  string
	Path    string
	Prefix  bool                     // true means match paths starting with this path
	Handler func(*Request) *Response // takes a request and returns a response
}

// stores all route rules in the order we add them
type Router struct {
	Routes []Route
}

// add route to the end of the list
func (router *Router) AddRoute(method string, path string, prefix bool, handler func(*Request) *Response) {
	router.Routes = append(router.Routes, Route{method, path, prefix, handler})
}

func (router *Router) Route(request *Request) *Response {
	// remove query string, then decode things like %20 into a space
	path := strings.SplitN(request.Path, "?", 2)[0]
	// example: /chat?name=Ryan becomes /chat before decoding
	path, err := url.PathUnescape(path)
	// reject invalid paths, backslashes, and null bytes
	if err != nil || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00") {
		return ErrorResponse(404, "Not Found")
	}

	// copy request so the original path stays available for debugging
	parsed := *request
	// * gets the value that the request pointer points to
	parsed.Path = path
	for _, route := range router.Routes {
		// skip this rule if the action is different, like POST instead of GET
		if route.Method != parsed.Method {
			continue
		}
		matches := parsed.Path == route.Path
		// prefix routes can match many files, like /public/imgs/dog.jpg
		if route.Prefix {
			matches = strings.HasPrefix(parsed.Path, route.Path)
		}
		// use the first matching route
		if matches {
			// & passes a pointer to the copied request into the handler
			return route.Handler(&parsed)
		}
	}
	// no rule matched the method and path
	return ErrorResponse(404, "Not Found")
}

// error responses use the same headers as regular responses
func ErrorResponse(code int, message string) *Response {
	response := NewResponse()
	response.SetStatus(code, message)
	response.SetText(message)
	return response
}
