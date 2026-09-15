# Corium — build, test and image targets.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Image coordinates
REGISTRY    ?= ghcr.io/qjoly
IMAGE_NAME  ?= corium
IMAGE_TAG   ?= dev
IMAGE       := $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

# Build metadata injected into the agent binary
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

GO_PACKAGES := ./...

##@ Go

.PHONY: build
build: ## Build the corium-agent binary for the host platform
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/corium-agent ./cmd/corium-agent

.PHONY: build-linux
build-linux: ## Build a static linux/amd64 agent for the OS image
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -trimpath -ldflags '$(LDFLAGS)' -o bin/linux-amd64/corium-agent ./cmd/corium-agent

.PHONY: test
test: ## Run unit tests
	go test -race -count=1 $(GO_PACKAGES)

.PHONY: test-update
test-update: ## Regenerate golden files
	go test -count=1 $(GO_PACKAGES) -update

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: fmt
fmt: ## Format the source tree
	gofmt -w -s .
	goimports -w .

.PHONY: tidy
tidy: ## Tidy and verify module dependencies
	go mod tidy
	go mod verify

##@ Image

.PHONY: image
image: ## Build the Corium OS container image
	podman build --tag $(IMAGE) --file Containerfile .

.PHONY: push
push: ## Push the OS image to the registry
	podman push $(IMAGE)

##@ Helpers

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
