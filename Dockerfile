# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install git and ca-certificates
RUN apk add --no-cache ca-certificates tzdata

# Fetch dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build statically compiled binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o wa-poll-bot main.go

# Production stage
FROM alpine:3.21

WORKDIR /app

# Install ca-certificates for TLS and tzdata for timezones
RUN apk add --no-cache ca-certificates tzdata

# Copy binary from builder
COPY --from=builder /app/wa-poll-bot /app/wa-poll-bot

ENTRYPOINT ["/app/wa-poll-bot"]
