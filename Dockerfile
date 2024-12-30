# Build stage
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application with platform-specific settings
ARG TARGETPLATFORM
ARG BUILDPLATFORM
RUN case "$TARGETPLATFORM" in \
      "linux/amd64") GOARCH=amd64 ;; \
      "linux/arm64") GOARCH=arm64 ;; \
      *) GOARCH=amd64 ;; \
    esac && \
    CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH go build -o main .

# Final stage
FROM --platform=$TARGETPLATFORM alpine:3.19

WORKDIR /app

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Copy the binary from builder
COPY --from=builder /app/main .

# Create logs directory
RUN mkdir -p /app/logs

# Volume for logs
VOLUME /app/logs

# Command to run the application
CMD ["./main"]