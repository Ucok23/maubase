.PHONY: run build test tidy

build:
	CGO_ENABLED=0 go build -o bin/baas ./cmd/baas

run:
	go run ./cmd/baas

test:
	go test ./...

tidy:
	go mod tidy
	gofmt -l -w .
