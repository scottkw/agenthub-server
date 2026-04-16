# syntax=docker/dockerfile:1.6
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/agenthub-server ./cmd/agenthub-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agenthub-server /usr/local/bin/agenthub-server
USER nonroot:nonroot
EXPOSE 443 80 3478/udp
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/agenthub-server"]
