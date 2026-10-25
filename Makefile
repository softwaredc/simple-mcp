# simple-mcp: build, test, bench.
#
# Both implementations are built the same way so a contributor never has to
# remember which directory needs what.

GO ?= go
CXX ?= g++

.PHONY: all build test bench clean fmt vet

all: build

# ---------------------------------------------------------------- Go

build: build-go build-cpp

build-go:
	cd go && $(GO) build ./...

test: test-go test-cpp

test-go:
	cd go && $(GO) test ./...

vet:
	cd go && $(GO) vet ./...

bench:
	cd go && $(GO) build -o /tmp/mcpbench ./cmd/mcpbench && /tmp/mcpbench -n 20000

fmt:
	cd go && $(GO) fmt ./...

# ---------------------------------------------------------------- C++

build-cpp:
	cmake -S cpp -B cpp/build -DCMAKE_BUILD_TYPE=Release
	cmake --build cpp/build -j

test-cpp:
	cmake -S cpp -B cpp/build -DCMAKE_BUILD_TYPE=Release
	cmake --build cpp/build -j
	cd cpp/build && ctest --output-on-failure

clean:
	rm -rf cpp/build
	cd go && $(GO) clean
