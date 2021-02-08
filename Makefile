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

.PHONY: grpc
grpc:
	protoc --go_out=. --go-grpc_out=. pkg/grpcstreamer/streamer.proto
	protoc --go_out=. --go_opt=paths=source_relative pkg/opentelemetry/proto/common/v1/common.proto
	protoc --go_out=. --go_opt=paths=source_relative pkg/opentelemetry/proto/metrics/v1/metrics.proto
	protoc --go_out=. --go_opt=paths=source_relative pkg/opentelemetry/proto/resource/v1/resource.proto

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

.PHONY: fuzz
fuzz:
	go-fuzz-build -o test/fuzzing/pkg-rec.zip ./pkg/rec
	go-fuzz -workdir=test/fuzzing -bin=test/fuzzing/pkg-rec.zip
