export GO111MODULE=on

all:
	go build -ldflags "-X main.version=$(shell git rev-parse HEAD)" ./cmd/nanotube

.PHONY: test
test:
	go test -cover -race ./...

.PHONY: lint
lint:
	golangci-lint run -E golint -E gofmt -E gochecknoglobals -E unparam -E misspell --exclude-use-default=false ./...

.PHONY: fmt
fmt:
	go fmt -s ./...

.PHONY: clean
clean:
	rm -f nanotube
