BIN := moddb-archiver

all: fmt vet test build

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

build:
	go build -o bin/$(BIN) .

clean:
	rm -rf bin

.PHONY: all fmt vet test build clean
