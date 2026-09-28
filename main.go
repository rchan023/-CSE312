package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func main() {
	// connect to PostgreSQL before accepting browser requests
	db, err := OpenDatabase()
	if err != nil {
		fmt.Println("Database error:", err)
		return
	}
	defer db.Close()
	fmt.Println("Connected to PostgreSQL")

	// create missing tables before the server starts
	if err := CreateTables(db); err != nil {
		fmt.Println("Database setup error:", err)
		return
	}
	fmt.Println("Database tables are ready")

	// keep file access inside public, including paths through links
	root, err := os.OpenRoot("public")
	if err != nil {
		fmt.Println("Error opening public folder:", err)
		return
	}
	defer root.Close()
	// set up the homepage, chat page, and public file routes once
	router := CreateRouter(root)
	// add chat routes that use PostgreSQL
	AddChatRoutes(router, db)

	// listen for browser connections on port 8080
	listener, err := net.Listen("tcp", ":8080")
	if err != nil {
		fmt.Println("Error starting server:", err)
		return
	}
	// defer schedules cleanup for when this function returns.
	defer listener.Close()

	fmt.Println("Server is listening on port 8080...")

	// Accept connections until server is stopped
	for {
		// Accept waits until client connects
		conn, err := listener.Accept()
		if err != nil {
			fmt.Println("Error accepting connection:", err)
			continue
		}

		// handles client while main accepts other clients.
		go handleConnection(conn, router)
	}
}

func handleConnection(conn net.Conn, router *Router) {
	// Close client's connection when handler finishes.
	defer conn.Close()

	// Give the client 30 seconds to send its request instead of waiting forever.
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		fmt.Println("Error setting read deadline:", err)
		return
	}
	// Collect complete headers and body, then use the parsed Request.
	request, err := ReadRequest(conn)
	if err != nil {
		fmt.Println("Error parsing request:", err)
		// tell the browser its request could not be read or understood
		writeResponse(conn, ErrorResponse(400, "Bad Request"))
		return
	}

	// log the route without printing secret cookies or message contents
	fmt.Printf("Parsed Request: Method: %s, Path: %s\n", request.Method, request.Path)

	// The response-building code and its comments now live in response.go.
	// choose the handler, then send the response it returns
	response := router.Route(request)
	writeResponse(conn, response)
}

func writeResponse(conn net.Conn, response *Response) {
	// stop waiting if the client is not receiving our response
	if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		fmt.Println("Error setting write deadline:", err)
		return
	}

	// send response as bytes, _ discards the returned byte count
	// Bytes builds the HTTP message; NewReader lets Copy read those bytes
	// Copy transfers the response to the connection and reports write errors
	_, err := io.Copy(conn, bytes.NewReader(response.Bytes()))
	if err != nil {
		fmt.Println("Error writing to connection:", err)
		return
	}
}
