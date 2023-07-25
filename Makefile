
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
	golangci-lint run -E gofmt -E gochecknoglobals -E unparam -E misspell --exclude-use-default=false --timeout 5m0s ./...

.PHONY: fmt
fmt:
	gofmt -d -s .

.PHONY: check
check: all test lint

.PHONY: end-to-end-test
end-to-end-test: docker-image
	docker run -it nanotube-test

.PHONY: clean
clean:
	rm -rf nanotube test/sender/sender test/receiver/receiver test/test2/{in,out}


got_go_fuzz := $(shell command -v go-fuzz 2> /dev/null)
got_go_fuzz_build := $(shell command -v go-fuzz-build 2> /dev/null)

.PHONY: check-go-fuzz
check-go-fuzz:
ifndef got_go_fuzz
	$(error "go-fuzz is not available please install it https://github.com/dvyukov/go-fuzz")
endif

.PHONY: check-go-fuzz-build
check-go-fuzz-build:
ifndef got_go_fuzz_build
	$(error "go-fuzz-build is not available please install it https://github.com/dvyukov/go-fuzz")
endif

.PHONY: fuzz
fuzz: check-go-fuzz check-go-fuzz-build
	go-fuzz-build -o test/fuzz/rec.zip ./pkg/rec
	go-fuzz -workdir=test/fuzz -bin=test/fuzz/rec.zip

.PHONY: fuzz-race
fuzz-race: check-go-fuzz check-go-fuzz-build
	go-fuzz-build -race -o test/fuzz/rec.zip ./pkg/rec
	go-fuzz -workdir=test/fuzz -bin=test/fuzz/rec.zip


test/sender/sender:
	go build -o ./test/sender/sender ./test/sender

test/receiver/receiver:
	go build -o ./test/receiver/receiver ./test/receiver

.PHONY: sender-linux
sender-linux:
	env GOOS=linux GOARCH=amd64 go build -o sender-linux ./test/sender

.PHONY: receiver-linux
receiver-linux:
	env GOOS=linux GOARCH=amd64 go build -o receiver-linux ./test/receiver

.PHONY: nanotube-linux
nanotube-linux:
	env GOOS=linux GOARCH=amd64 go build -o nanotube-linux -ldflags "-X main.version=$(shell git rev-parse HEAD)" ./cmd/nanotube

.dockerignore: .gitignore
	cat .gitignore | grep -v .dockerignore > .dockerignore

.PHONY: docker-image
docker-image: .dockerignore
	docker build -f test/Dockerfile  -t nanotube-test .

.PHONY: local-end-to-end-test
local-end-to-end-test: nanotube test/sender/sender test/receiver/receiver
	cd test && ./run.sh

.PHONY: build-for-benchmarking-setup
build-for-benchmarking-setup: nanotube-linux sender-linux receiver-linux
	mv nanotube-linux test/performance/roles/nanotube/files/nanotube
	mv sender-linux test/performance/roles/sender/files/sender
	mv receiver-linux test/performance/roles/receiver/files/receiver
