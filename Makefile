.PHONY: deps lint test build run docker clean tidy fmt vet cover

BIN       ?= bin/netmantle
PKG       := ./...
GOFILES   := $(shell find . -type f -name '*.go' -not -path './vendor/*')
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -X github.com/i4Edu/netmantle/internal/version.Version=$(VERSION)

deps:
	go mod download

tidy:
	go mod tidy

fmt:
	gofmt -s -w $(GOFILES)

vet:
	go vet $(PKG)

lint:
	@unformatted="$$(gofmt -s -l $(GOFILES))"; \
	if [ -n "$$unformatted" ]; then \
		echo "Files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi
	go vet $(PKG)

test:
	go test -race -count=1 $(PKG)

cover:
	go test -race -count=1 -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -n 1

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/netmantle

run: build
	./$(BIN) serve --config config.example.yaml

docker:
	docker build -t netmantle:$(VERSION) .

clean:
	rm -rf bin dist coverage.out coverage.html data/*.db data/*.db-journal
