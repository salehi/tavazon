# tavazon — build & deployment automation.
#
# The host needs no Go toolchain: by default every Go command runs inside the
# same golang image the Dockerfile uses, as your own user, with output and
# caches written back into the working tree (so nothing is root-owned). To use
# a local Go toolchain instead, override it, e.g.  `make build GO=go`.

VERSION  ?= dev
GO_IMAGE ?= golang:1.22
IMAGE    ?= salehi/tavazon:latest
COMPOSE  ?= docker compose

BIN     := tavazon
PKG     := ./cmd/tavazon
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath -mod=vendor

# Run the Go toolchain inside the build image as the current user. GOCACHE lives
# under .cache/ in the tree so repeat builds stay fast; GOPATH is throwaway since
# dependencies are vendored. Override GO=go to use a native toolchain instead.
GO ?= docker run --rm \
	-u $$(id -u):$$(id -g) \
	-v "$(CURDIR)":/src -w /src \
	-e HOME=/tmp -e GOPATH=/tmp/go \
	-e GOCACHE=/src/.cache/go-build -e CGO_ENABLED=0 \
	$(GO_IMAGE) go

.DEFAULT_GOAL := help

.PHONY: help
help: ## List the available targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

# ---- Go toolchain (runs in the build image unless GO=go) ----

.PHONY: build
build: ## Compile the binary into bin/tavazon
	@mkdir -p bin
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BIN) $(PKG)

.PHONY: test
test: ## Run the test suite
	$(GO) test -mod=vendor ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet -mod=vendor ./...

.PHONY: fmt
fmt: ## Format the Go sources in place
	$(GO) fmt ./...

.PHONY: check
check: fmt vet test ## Format, vet, and test

# ---- container image & service ----

.PHONY: image
image: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

.PHONY: up
up: ## Build and start the service via docker compose
	$(COMPOSE) up -d --build

.PHONY: down
down: ## Stop the service
	$(COMPOSE) down

.PHONY: logs
logs: ## Follow the service logs
	$(COMPOSE) logs -f

# ---- housekeeping ----

.PHONY: clean
clean: ## Remove build artifacts and the local build cache
	rm -rf bin .cache
