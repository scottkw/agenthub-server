.PHONY: build test lint run clean headscale-bin admin-build

BINARY := bin/agenthub-server
PKG := ./...
ADMIN_DIR := web/admin

admin-build:
	cd $(ADMIN_DIR) && npm install && npm run build
	cp -r $(ADMIN_DIR)/dist internal/admin/dist

build: admin-build
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
