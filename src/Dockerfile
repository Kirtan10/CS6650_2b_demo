# ---- Build stage: compile a static Go binary ----
FROM golang:1.23-alpine AS build
WORKDIR /app

# Copy module files first so `go mod download` is cached between builds
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /product-service .

# ---- Runtime stage: tiny image with just the binary ----
FROM alpine:3.20
RUN adduser -D -u 10001 appuser
COPY --from=build /product-service /product-service
USER appuser

ENV GIN_MODE=release \
    PORT=8080
EXPOSE 8080
ENTRYPOINT ["/product-service"]
