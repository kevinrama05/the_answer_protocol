# TAP — build tool (Go modules + make).
#   make install          check the Go toolchain and download/verify modules
#   make build            compile the three binaries into ./bin
#   make run-server       start the server            (PORT, WORLD, LOG_FILE)
#   make run-client       start the CLI client        (HOST, PORT, NAME)
#   make run-client-gui   start the GUI client        (HOST, PORT, GUI_ADDR)
#   make lint             gofmt + go vet (+ staticcheck when installed)
#   make test             unit and integration tests
#   make clean / fclean   remove build artefacts

GO        ?= go
BIN       := bin
HOST      ?= localhost
PORT      ?= 4242
NAME      ?=
WORLD     ?= world/world.json
LOG_FILE  ?=
GUI_ADDR  ?= 127.0.0.1:8080

SERVER := $(BIN)/tap-server
CLIENT := $(BIN)/tap-client
GUI    := $(BIN)/tap-gui

.PHONY: all install build run-server run-client run-client-gui lint test check-world clean fclean re

all: build

install:
	@command -v $(GO) >/dev/null 2>&1 || { echo "Go is not installed: see https://go.dev/doc/install"; exit 1; }
	@$(GO) version
	$(GO) mod download
	$(GO) mod verify

build:
	@mkdir -p $(BIN)
	$(GO) build -o $(SERVER) ./server
	$(GO) build -o $(CLIENT) ./client
	$(GO) build -o $(GUI) ./gui

run-server: build
	./$(SERVER) -addr :$(PORT) -world $(WORLD) $(if $(LOG_FILE),-log-file $(LOG_FILE))

run-client: build
	./$(CLIENT) -host $(HOST) -port $(PORT) $(if $(NAME),-name $(NAME))

run-client-gui: build
	./$(GUI) -host $(HOST) -port $(PORT) -listen $(GUI_ADDR)

check-world: build
	./$(SERVER) -check -world $(WORLD)

lint:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi
	$(GO) vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; \
	else echo "staticcheck not installed, skipped (go install honnef.co/go/tools/cmd/staticcheck@latest)"; fi

test:
	$(GO) test -count=1 ./...

clean:
	$(GO) clean ./...
	rm -rf $(BIN)

fclean: clean
	$(GO) clean -testcache

re: fclean build
