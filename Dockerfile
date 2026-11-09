# syntax=docker/dockerfile:1
# Build stage: compile static binary
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/rss-scraper ./cmd/rss-scraper

# Runtime stage: minimal image
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 568 app
COPY --from=build /out/rss-scraper /usr/local/bin/rss-scraper
USER app
WORKDIR /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/rss-scraper"]
