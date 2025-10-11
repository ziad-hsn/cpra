# Build stage
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set up Go toolchain to download required version
ENV GOTOOLCHAIN=go1.25.0

# Set the working directory
WORKDIR /app

# Copy go mod files
COPY go.mod .
COPY go.sum .

# Download dependencies
RUN go mod download

# Copy source code
COPY internal internal
COPY samples samples
COPY main.go .

RUN ls

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o cpra ./main.go

# Final stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates tzdata

# Create a non-root user
RUN addgroup -g 1001 -S cpra && \
    adduser -u 1001 -S cpra -G cpra

# Set the working directory
WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/cpra .

# Copy samples directory
COPY --from=builder /app/samples ./samples

# Change ownership to non-root user
RUN chown -R cpra:cpra /app

# Switch to non-root user
USER cpra

# Expose any necessary ports (if needed)
# EXPOSE 8080

# Set environment variables for memory limits
ENV GOMEMLIMIT=1GiB
ENV GOGC=100

# Default command - can be overridden via docker-compose.yml
CMD ["./cpra", "--yaml", "samples/replicated_test_10k.yaml"]
