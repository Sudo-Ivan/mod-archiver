BIN := mod-archiver

all: fmt vet test lint build

fmt:
	gofmt -s -w .

vet:
	go vet ./...

lint:
	golangci-lint run ./...

test:
	go test ./...

build:
	go build -o bin/$(BIN) .

clean:
	rm -rf bin

.PHONY: all fmt vet lint test build clean
