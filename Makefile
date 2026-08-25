.PHONY: run build test tidy

build:
	CGO_ENABLED=0 go build -o bin/maubase ./cmd/maubase

run:
	go run ./cmd/maubase

test:
	go test ./...

tidy:
	go mod tidy
	gofmt -l -w .
