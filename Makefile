GO ?= go
PNPM ?= pnpm
PYTHON ?= python3
BUILD_TAGS ?=
ALL_DRIVER_TAGS = redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio
BUILD_DIR ?= bin
RELEASE_DIR ?= dist/release
VERSION ?= $(shell if test -e .git; then git describe --tags --always --dirty 2>/dev/null || echo dev; else echo dev; fi)
COMMIT ?= $(shell if test -e .git; then git rev-parse --short HEAD 2>/dev/null || echo unknown; else echo unknown; fi)
BUILD_VCS = $(shell if test -e .git; then echo true; else echo false; fi)
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_FLAGS = -X cpra/internal/version.Version=$(VERSION) -X cpra/internal/version.Commit=$(COMMIT) -X cpra/internal/version.Date=$(DATE)

.DEFAULT_GOAL := all
.PHONY: all build build-ctl dashboard-build dashboard-check fmt-check vet test check test-all-drivers release clean

all: build build-ctl

build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=$(BUILD_VCS) -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS)" -o $(BUILD_DIR)/cpra .

build-ctl:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=$(BUILD_VCS) -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS)" -o $(BUILD_DIR)/cpractl ./cmd/cpractl

dashboard-build:
	cd dashboard && $(PNPM) install --frozen-lockfile
	cd dashboard && $(PNPM) build
	$(PYTHON) -B scripts/release/stage_dashboard.py

dashboard-check:
	cd dashboard && $(PNPM) exec tsc --noEmit
	cd dashboard && $(PNPM) lint
	cd dashboard && $(PNPM) test

fmt-check:
	@test -z "$$(gofmt -l $$(find internal cmd -name '*.go') main.go)"

vet:
	$(GO) vet ./...

test:
	$(GO) test -race ./...

check: fmt-check vet test

test-all-drivers:
	$(GO) test -race -tags "$(ALL_DRIVER_TAGS)" ./...

release: dashboard-build
	@mkdir -p $(BUILD_DIR)
	@set -e; for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -buildvcs=$(BUILD_VCS) -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS) -s -w" -o $(BUILD_DIR)/cpra-linux-$$arch .; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -buildvcs=$(BUILD_VCS) -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS) -s -w" -o $(BUILD_DIR)/cpractl-linux-$$arch ./cmd/cpractl; \
	done
	$(PYTHON) -B scripts/release/package.py --version "$(VERSION)" --tags "$(BUILD_TAGS)" --bin-dir "$(BUILD_DIR)" --out "$(RELEASE_DIR)"

clean:
	rm -rf bin dist dashboard/dist

.PHONY: build-verification verify-local benchmark-preflight
build-verification:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS)" -o $(BUILD_DIR)/cpra-verify ./cmd/cpra-verify
	$(GO) build -trimpath -o $(BUILD_DIR)/cpra-target ./cmd/cpra-target

verify-local: build-verification
	@mkdir -p evidence/local
	$(PYTHON) -B scripts/verification/local.py --binary $(BUILD_DIR)/cpra-verify --out evidence/local/providers.json

benchmark-preflight:
	$(PYTHON) -B scripts/benchmark/campaign.py --mode preflight --out evidence/local/preflight
