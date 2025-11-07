# Build stage
FROM golang:alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the applications
RUN go build -o server.run ./cmd/server && \
    go build -o crawl.run ./cmd/sub

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy binaries from builder
COPY --from=builder /app/server.run .
COPY --from=builder /app/crawl.run .

# Expose port
EXPOSE 3000

# Run the binary
CMD ["./server.run"]
