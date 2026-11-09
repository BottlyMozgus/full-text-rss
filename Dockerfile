# syntax=docker/dockerfile:1
# Build stage: compile static binary
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/full-text-rss ./cmd/full-text-rss

# Runtime stage: minimal image
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 568 app
COPY --from=build /out/full-text-rss /usr/local/bin/full-text-rss
USER app
WORKDIR /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/full-text-rss"]
