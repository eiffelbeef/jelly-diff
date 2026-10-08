.PHONY: build run lint test clean docker docker-push

BINARY     := jelly-diff
CMD_PATH   := ./cmd/jelly-diff
DOCKER_IMG := ghcr.io/eiffelbeef/jelly-diff
VERSION    := $(shell git describe --tags --always 2>/dev/null || echo "dev")

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BINARY) $(CMD_PATH)

run:
	go run $(CMD_PATH)

lint:
	golangci-lint run ./...

test:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

clean:
	rm -f $(BINARY) coverage.out

docker:
	docker build -t $(DOCKER_IMG):$(VERSION) -t $(DOCKER_IMG):latest .

docker-push:
	docker buildx build \
	  --platform linux/amd64,linux/arm64 \
	  --tag $(DOCKER_IMG):$(VERSION) \
	  --tag $(DOCKER_IMG):latest \
	  --push .

tidy:
	go mod tidy
