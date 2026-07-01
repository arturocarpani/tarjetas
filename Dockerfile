FROM golang:alpine AS builder

WORKDIR /app

COPY . .

# Build the application
RUN go build -o expenseowl ./cmd/expenseowl

# Use a minimal alpine image for running
FROM alpine:latest

WORKDIR /app

# Root CA bundle for outbound HTTPS (Telegram + Anthropic APIs) and tzdata so
# timestamps aren't stuck in UTC.
RUN apk add --no-cache ca-certificates tzdata

# Create data directory if not exists
RUN mkdir -p /app/data

# Copy the binary from builder
COPY --from=builder /app/expenseowl .

# Expose the default port
EXPOSE 8080

# Run the server
CMD ["./expenseowl"]
