
# a phony target to always have go check and maybe rebuild nanotube
.PHONY: all
all: build

# a file target ensuring nanotube is there
# no rebuilding if present (useful for tests)
nanotube:
	go build -ldflags "-X main.version=$(shell git rev-parse HEAD)" ./cmd/nanotube

.PHONY: build
build:
	go build -ldflags "-X main.version=$(shell git rev-parse HEAD)" ./cmd/nanotube

.PHONY: race
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
check: all test end-to-end-test lint

.PHONY: end-to-end-test
end-to-end-test: docker-image
	docker run -it nanotube-test

.PHONY: clean
clean:
	git clean -Xf

.PHONY: fuzz
fuzz:
	go-fuzz-build -o test/fuzzing/pkg-rec.zip ./pkg/rec
	go-fuzz -workdir=test/fuzzing -bin=test/fuzzing/pkg-rec.zip

test/sender/sender: test/sender/sender.go
	go build -o $@ $<

test/receiver/receiver: test/receiver/receiver.go
	go build -o $@ $<

.dockerignore: .gitignore
	cat .gitignore | grep -v .dockerignore > .dockerignore

.PHONY: docker-image
docker-image: .dockerignore
	docker build -f test/Dockerfile  -t nanotube-test .

.PHONY: local-end-to-end-test
local-end-to-end-test: nanotube test/sender/sender test/receiver/receiver
	cd test && ./run.sh
