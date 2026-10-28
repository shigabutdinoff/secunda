# Build stage
FROM golang:1.27.3-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/server ./cmd/server

# Runtime stage
FROM alpine:3.24.3
WORKDIR /app
COPY --from=builder /out/server .
COPY migrations ./migrations
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["./server"]
