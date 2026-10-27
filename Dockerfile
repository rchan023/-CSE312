# use the same Go version as go.mod
FROM golang:1.27.1-bookworm

# install media tools before copying source so this layer stays cached
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg curl && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /hls && chmod 777 /hls

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY public ./public
RUN go build -buildvcs=false -o server .
EXPOSE 8080
CMD ["./server"]
