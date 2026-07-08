# Canonical build for projx-engine. The version is stamped from git — never
# hardcoded in source — via -ldflags. Use `make build` / `make install` (and the
# release build) so every binary reports the correct version.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
export GOWORK = off

BIN := projx-engine
DEST ?= $(HOME)/.local/bin/$(BIN)

.PHONY: build install version clean

build:                ## build ./$(BIN) with the git-stamped version
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

install:              ## build + install to ~/.local/bin (git-stamped version)
	go build -ldflags "$(LDFLAGS)" -o $(DEST) .
	@echo "installed $(VERSION) -> $(DEST)"

version:              ## print the version that would be stamped
	@echo $(VERSION)

clean:
	rm -f $(BIN)
