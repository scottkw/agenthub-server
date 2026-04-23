# Stage 1: Build admin SPA
FROM node:24-alpine AS admin-builder
WORKDIR /app
COPY web/admin/package.json web/admin/package-lock.json ./
RUN npm ci
COPY web/admin/ .
RUN npm run build

# Stage 2: Build Go binary
FROM golang:1.26-alpine AS go-builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Copy built admin dist so //go:embed succeeds
COPY --from=admin-builder /app/dist internal/admin/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(git describe --tags --always 2>/dev/null || echo dev)" -o /bin/agenthub-server ./cmd/agenthub-server

# Stage 3: Minimal runtime image
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=go-builder /bin/agenthub-server /agenthub-server
EXPOSE 443 80 3478
ENTRYPOINT ["/agenthub-server"]
