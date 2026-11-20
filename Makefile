BIN := itop
LDFLAGS := -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
	-X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) \
	-X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: build install vet test

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/itop

install: build
	mkdir -p $(HOME)/.local/bin
	install -m 0755 bin/$(BIN) $(HOME)/.local/bin/$(BIN)

vet:
	go vet ./...

test:
	go test ./...
