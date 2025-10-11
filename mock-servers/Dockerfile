FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY main.go .

# Build with optimizations for production
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-w -s' -o main main.go

FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -S appgroup && \
    adduser -S appuser -G appgroup

WORKDIR /app
COPY --from=builder /app/main .
COPY --chown=appuser:appgroup --from=builder /app/main .

# Use non-root user for security
USER appuser

# Health check for Swarm
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://localhost:9999/stats || exit 1

EXPOSE 10000-60000

CMD ["./main"]

