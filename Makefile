.PHONY: run build test tidy e2e

build:
	CGO_ENABLED=0 go build -o bin/maubase ./cmd/maubase

run:
	go run ./cmd/maubase

test:
	go test ./...

tidy:
	go mod tidy
	gofmt -l -w .

# e2e drives the embedded admin UI in a real (headless) browser via
# Playwright and builds a storyboard report — a screenshot per step plus
# the full session video — see e2e/README.md. npm install/playwright
# install are cheap no-ops once cached, so this is safe to run repeatedly,
# including from a fresh checkout or a fresh agent with no local state.
e2e:
	cd e2e && npm install && npx playwright install chromium && npm run test:report
