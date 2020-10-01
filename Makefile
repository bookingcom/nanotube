.PHONY: all
all:
	go build -ldflags "-X main.version=$(shell git rev-parse HEAD)" ./cmd/nanotube

race:
	go build -race -ldflags "-X main.version=$(shell git rev-parse HEAD)" ./cmd/nanotube

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
check: all test lint fuzz

.PHONY: clean
clean:
	rm -f nanotube

.PHONY: fuzz
fuzz:
	go-fuzz-build ./pkg/rec
	go-fuzz -bin=rec-fuzz.zip