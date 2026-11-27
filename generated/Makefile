# Builds every binary of this deliverable: one target per directory that holds a
# service (or the gateway) in this tree. Binaries land in core/bin/.
GOOS   ?= linux
GOARCH ?= amd64
export GOOS GOARCH

.PHONY: all build test clean help

all: build

build:
	$(MAKE) -C core/cmd/nwc620-services build
	$(MAKE) -C core/cmd/nwc700-services build
	@echo "Build completed."

test:
	go test ./...

clean:
	$(MAKE) -C core/cmd/nwc620-services clean
	$(MAKE) -C core/cmd/nwc700-services clean

help:
	@echo "build : builds every binary (default)"
	@echo "test  : go test ./..."
	@echo "clean : removes the binaries"
