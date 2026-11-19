BINARY      := swhatis
INSTALL_DIR := $(HOME)/.local/bin
MAN_DIR     := $(HOME)/.local/share/man/man1
GO_BIN_DIR  := $(HOME)/workspace/go/bin
LDFLAGS     := -s -w

.PHONY: all build test vet clean install deploy completions

all: vet test build

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) .

test:
	go test -v -race ./...

vet:
	go vet ./...

install: build
	@mkdir -p $(INSTALL_DIR) $(MAN_DIR)
	cp $(BINARY) $(INSTALL_DIR)/$(BINARY)
	cp $(BINARY).1 $(MAN_DIR)/$(BINARY).1
	@if [ -d "$(GO_BIN_DIR)" ]; then cp $(BINARY) $(GO_BIN_DIR)/$(BINARY); fi
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY) and man page to $(MAN_DIR)/$(BINARY).1"

deploy: install

completions: build
	@mkdir -p completions
	./$(BINARY) -completion zsh > completions/_swhatis
	./$(BINARY) -completion bash > completions/swhatis.bash

clean:
	rm -f $(BINARY)
