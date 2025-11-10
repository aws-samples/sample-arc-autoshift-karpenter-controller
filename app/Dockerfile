# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app
ENV GOPROXY=direct
ENV GOSUMDB=off
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o sns-subscriber main.go && \
    go clean -cache -modcache

# Create a non-root user
RUN echo "appuser:x:10000:10000::/home/appuser:/sbin/nologin" >> /etc/passwd

# Final minimal image
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /app/sns-subscriber /sns-subscriber

USER 10000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["./sns-subscriber", "-health"]

CMD ["/sns-subscriber"]
