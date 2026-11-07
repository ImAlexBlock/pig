GO ?= go
BIN ?= ./pig
PKG := ./...

.PHONY: all build vet test fmt clean install bench

all: build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/pig

vet:
	$(GO) vet $(PKG)

test:
	$(GO) test $(PKG)

fmt:
	$(GO) fmt $(PKG)

clean:
	rm -f $(BIN) ./pig.exe

install:
	CGO_ENABLED=0 $(GO) install ./cmd/pig

# Agent benchmark: needs a hermesbench checkout and a configured PIG_HOME.
bench:
	bench/.venv/bin/python bench/run_hermesbench.py --suite $${SUITE:-/tmp/hermesbench} \
		--run-id $${RUN_ID:-pig-$$(date +%Y%m%d-%H%M%S)} --workers $${WORKERS:-4}
