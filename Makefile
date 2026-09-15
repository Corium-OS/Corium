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

##@ Installable artefacts

# bootc-image-builder turns the OS image into something you can actually boot.
# It needs a privileged container and access to the local image store, and it
# must run on a Linux host: it mounts the root filesystem it creates in order to
# populate it.
BIB          ?= quay.io/centos-bootc/bootc-image-builder:latest
OUTPUT_DIR   ?= $(CURDIR)/output
ARTEFACTS    ?= qcow2 raw anaconda-iso

define bib
	sudo podman run --rm --privileged \
		--security-opt label=type:unconfined_t \
		-v /var/lib/containers/storage:/var/lib/containers/storage \
		-v $(OUTPUT_DIR):/output \
		$(BIB) --type $(1) $(IMAGE)
endef

.PHONY: artefacts
artefacts: $(addprefix artefact-,$(ARTEFACTS)) ## Build every installable artefact

.PHONY: artefact-qcow2
artefact-qcow2: ## Build a qcow2 for Proxmox, KVM and libvirt
	@mkdir -p $(OUTPUT_DIR)
	$(call bib,qcow2)

.PHONY: artefact-raw
artefact-raw: ## Build a raw disk for bare metal and most clouds
	@mkdir -p $(OUTPUT_DIR)
	$(call bib,raw)

.PHONY: artefact-anaconda-iso
artefact-anaconda-iso: ## Build an installer ISO for bare metal
	@mkdir -p $(OUTPUT_DIR)
	$(call bib,anaconda-iso)

.PHONY: clean-artefacts
clean-artefacts: ## Remove built artefacts
	sudo rm -rf $(OUTPUT_DIR)

##@ Helpers

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
