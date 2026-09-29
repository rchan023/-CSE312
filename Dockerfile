# use the same Go version as go.mod
FROM golang:1.27.1-bookworm

# run commands from this folder inside the container
WORKDIR /app

# download packages
COPY go.mod go.sum ./
RUN go mod download

# copy code into container
COPY *.go ./
COPY public ./public

# compile server inside container
RUN go build -buildvcs=false -o server .

EXPOSE 8080
CMD ["./server"]
