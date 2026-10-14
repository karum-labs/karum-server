# karum-server — Go + Gin API gateway.
#
# Targets here run locally without Docker. For the full stack, use the Makefile
# in the infra repo, which brings up every service via Compose.

GO ?= go
GOFMT ?= gofmt

.PHONY: run build test fmt lint tidy clean

## run: build and start the gateway on the default port.
run:
	$(GO) run ./cmd/gateway

## build: compile the gateway binary into bin/.
build:
	$(GO) build -o bin/gateway ./cmd/gateway

## test: run all unit tests with the race detector.
test:
	$(GO) test -race ./...

## fmt: rewrite Go files in place with gofmt.
fmt:
	$(GOFMT) -w .

## lint: fail if any file is unformatted or vet reports a problem.
lint:
	@files=$$($(GOFMT) -l .); \
	if [ -n "$$files" ]; then \
		echo "gofmt needed on:"; echo "$$files"; exit 1; \
	fi
	$(GO) vet ./...

## tidy: sync go.mod and go.sum.
tidy:
	$(GO) mod tidy

## clean: remove build artifacts.
clean:
	rm -rf bin/
