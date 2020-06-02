.PHONY: all
all:
	go build -ldflags "-X main.version=$(shell git rev-parse HEAD)" ./cmd/nanotube

.PHONY: install
install:
	go install ./cmd/nanotube
	go install ./test/receiver
	go install ./test/sender

.PHONY: test
test:
	go test -cover -race ./...

.PHONY: lint
lint:
	golangci-lint run -E golint -E gofmt -E gochecknoglobals -E unparam -E misspell --exclude-use-default=false ./...

.PHONY: fmt
fmt:
	gofmt -d -s .

.PHONY: check
check: all test lint

.PHONY: clean
clean:
	rm -f nanotube
