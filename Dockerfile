# use the same Go version as go.mod
FROM golang:1.27.1-bookworm

# run commands from this folder inside the container
WORKDIR /app

# download packages before copying code so rebuilds can reuse this step
COPY go.mod go.sum ./
RUN go mod download

# copy the server code and frontend
COPY *.go ./
COPY public ./public

# compile the server inside the container
RUN go build -buildvcs=false -o server .

# run the server as an unprivileged user
USER nobody

# the Go server listens on this port
EXPOSE 8080
CMD ["./server"]
