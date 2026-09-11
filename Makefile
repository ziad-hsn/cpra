GO ?= go
PNPM ?= pnpm
PYTHON ?= python3
BUILD_TAGS ?=
ALL_DRIVER_TAGS = redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio
BUILD_DIR ?= bin
RELEASE_DIR ?= dist/release
# Development builds use Go's native module/VCS metadata. Official release
# identities and source dates come only from the checked RELEASE.json recipe.
VERSION ?= v0.0.0-dev
RELEASE_GO ?= $(GO)
GORELEASER ?= bin/release-tools/goreleaser
NFPM ?= bin/release-tools/nfpm
VERSION_FLAGS ?=
BUILD_VCS ?= auto

.DEFAULT_GOAL := all
.PHONY: all build build-ctl dashboard-build dashboard-check fmt-check vet test check test-all-drivers release clean release-prepare release-build release-build-goreleaser release-package release-check release-tools

all: build build-ctl

build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=$(BUILD_VCS) -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS)" -o $(BUILD_DIR)/cpra .

build-ctl:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=$(BUILD_VCS) -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS)" -o $(BUILD_DIR)/cpractl ./cmd/cpractl

dashboard-build:
	$(PYTHON) -B scripts/release/dashboard_build.py --pnpm "$(PNPM)"

dashboard-check:
	cd dashboard && $(PNPM) exec tsc --noEmit
	cd dashboard && $(PNPM) lint
	cd dashboard && $(PNPM) test

fmt-check:
	@test -z "$$(gofmt -l $$(find internal cmd -name '*.go') $$(find . -maxdepth 1 -name '*.go'))"

vet:
	$(GO) vet ./...

test:
	$(GO) test -race ./...

check: fmt-check vet test

test-all-drivers:
	$(GO) test -race -tags "$(ALL_DRIVER_TAGS)" ./...

# Release preparation requires committed, regenerated dashboard assets. It
# never mutates the source tree. Use RELEASE_CANDIDATE=--candidate for local
# dirty-tree smoke builds; those builds cannot produce source-release archives.
RELEASE_CANDIDATE ?=
release-prepare:
	$(PYTHON) -B scripts/release/release.py prepare --version "$(VERSION)" --out "$(RELEASE_DIR)" $(RELEASE_CANDIDATE)

release-build:
	$(PYTHON) -B scripts/release/release.py build --go "$(RELEASE_GO)" --out "$(RELEASE_DIR)"

release-build-goreleaser:
	$(PYTHON) -B scripts/release/release.py build --go "$(RELEASE_GO)" --goreleaser "$(GORELEASER)" --out "$(RELEASE_DIR)"

release-package:
	$(PYTHON) -B scripts/release/release.py pack --out "$(RELEASE_DIR)"
	$(PYTHON) -B scripts/release/linux_packages.py --out "$(RELEASE_DIR)" --nfpm "$(NFPM)"

release-check:
	$(PYTHON) -B -m unittest discover -s scripts/release -p 'test_*.py'
	$(GO) test ./internal/version

release-tools:
	$(PYTHON) -B scripts/release/install_tools.py

release:
	$(MAKE) release-prepare
	$(MAKE) release-build-goreleaser
	$(MAKE) release-package

clean:
	rm -rf bin dist dashboard/dist

.PHONY: build-verification verify-local verify-contracts verify-protocols benchmark-preflight
build-verification:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS)" -o $(BUILD_DIR)/cpra-verify ./cmd/cpra-verify
	$(GO) build -trimpath -o $(BUILD_DIR)/cpra-target ./cmd/cpra-target

verify-local: build-verification
	@mkdir -p evidence/local
	$(PYTHON) -B scripts/verification/local.py --binary $(BUILD_DIR)/cpra-verify --out evidence/local/providers.json

# Socket-level fixtures require no accounts or Docker. Include optional HTTP
# notification drivers in contract builds with BUILD_TAGS="teams twilio".
verify-contracts: build-verification
	@mkdir -p evidence/local
	$(PYTHON) -B scripts/verification/contracts.py --binary $(BUILD_DIR)/cpra-verify --out evidence/local/contracts.json

verify-protocols: build-verification
	@mkdir -p evidence/local
	$(PYTHON) -B scripts/verification/protocol_local.py --binary $(BUILD_DIR)/cpra-verify --out evidence/local/protocols.json

benchmark-preflight:
	$(PYTHON) -B scripts/benchmark/campaign.py --mode preflight --out evidence/local/preflight
