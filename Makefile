.PHONY: build test lint run clean headscale-bin

BINARY := bin/agenthub-server
PKG := ./...

build:
	go build -o $(BINARY) ./cmd/agenthub-server

test:
	go test -race -timeout 60s $(PKG)

lint:
	go vet $(PKG)
	gofmt -l -d .

run: build
	./$(BINARY) --config config.example.yaml

clean:
	rm -rf bin/ coverage.out

headscale-bin:
	./scripts/fetch-headscale.sh
