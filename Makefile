GO ?= go
PNPM ?= pnpm
PYTHON ?= python3
BUILD_TAGS ?=
# The full management race suite includes the measured large encrypted-input
# fixture. Its combined runtime exceeds Go's default ten-minute package limit.
GO_TEST_TIMEOUT ?= 20m
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
# Make's development commands build the application and unpublished nested SDK
# modules together. Explicit GOWORK (including off) always takes precedence.
# This is command-local: release tools retain their isolated recorded recipe.
DEV_GOWORK = $(if $(strip $(GOWORK)),$(GOWORK),$(abspath $(BUILD_DIR)/cpra-sdk.work))
DEV_GO = GOWORK="$(DEV_GOWORK)" $(GO)

.DEFAULT_GOAL := all
.PHONY: all build build-ctl dashboard-build dashboard-check fmt-check vet test check test-all-drivers release clean release-prepare release-build release-build-goreleaser release-package release-check release-tools

all: build build-ctl

.PHONY: dev-workspace
dev-workspace:
ifeq ($(strip $(GOWORK)),)
	$(PYTHON) -B scripts/sdk/workspace.py --output "$(DEV_GOWORK)" --no-github-env
endif

build: dashboard-freshness | dev-workspace
	@mkdir -p "$(BUILD_DIR)"
	CGO_ENABLED=0 $(DEV_GO) build -trimpath -buildvcs=$(BUILD_VCS) -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS)" -o "$(BUILD_DIR)/cpra" .

build-ctl: | dev-workspace
	@mkdir -p "$(BUILD_DIR)"
	CGO_ENABLED=0 $(DEV_GO) build -trimpath -buildvcs=$(BUILD_VCS) -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS)" -o "$(BUILD_DIR)/cpractl" ./cmd/cpractl

dashboard-build:
	$(PYTHON) -B scripts/release/dashboard_build.py --pnpm "$(PNPM)" --go "$(RELEASE_GO)"

.PHONY: dashboard-freshness
dashboard-freshness:
	$(PYTHON) -B scripts/dashboard/asset_manifest.py

dashboard-check:
	cd dashboard && $(PNPM) exec tsc --noEmit
	cd dashboard && $(PNPM) lint
	cd dashboard && $(PNPM) test

fmt-check:
	@unformatted="$$(gofmt -l internal cmd *.go)" || exit $$?; if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi

vet: | dev-workspace
	$(DEV_GO) vet ./...

test: | dev-workspace
	$(DEV_GO) test -race -timeout "$(GO_TEST_TIMEOUT)" ./...

check: fmt-check vet test

test-all-drivers: | dev-workspace
	$(DEV_GO) test -race -timeout "$(GO_TEST_TIMEOUT)" -tags "$(ALL_DRIVER_TAGS)" ./...

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
build-verification: | dev-workspace
	@mkdir -p "$(BUILD_DIR)"
	$(DEV_GO) build -trimpath -tags "$(BUILD_TAGS)" -ldflags="$(VERSION_FLAGS)" -o "$(BUILD_DIR)/cpra-verify" ./cmd/cpra-verify
	$(DEV_GO) build -trimpath -o "$(BUILD_DIR)/cpra-bench-target" ./cmd/cpra-bench-target

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

# Source verification uses the same development workspace as normal builds.
# Official releases keep GOWORK=off; local workspaces do not qualify published
# module dependencies or downloaded release artifacts.
.PHONY: sdk-check sdk-consumer-check
sdk-check: | dev-workspace
	cd sdk/go && $(DEV_GO) test -race ./...
	cd sdk/go && $(DEV_GO) test -race -tags externaljobs ./...
	cd sdk/go/worker && $(DEV_GO) test -race -tags externaljobs ./...
	cd sdk/go && $(DEV_GO) vet ./...
	cd sdk/go && $(DEV_GO) vet -tags externaljobs ./...
	cd sdk/go/worker && $(DEV_GO) vet -tags externaljobs ./...

sdk-consumer-check: | dev-workspace
	$(PYTHON) -B -m unittest discover -s scripts/sdk -p 'test_*.py'
	$(DEV_GO) test ./scripts/sdk/doccheck
	$(PYTHON) -B scripts/sdk/verify.py --go "$(GO)" --out evidence/local/sdk-consumer.json

.PHONY: sdk-examples-check sdk-reference-check
sdk-examples-check:
	$(PYTHON) scripts/sdk/verify_examples.py --go "$(GO)" --race --out evidence/local/sdk-examples.json

sdk-reference-check: | dev-workspace
	GOWORK="$(DEV_GOWORK)" $(PYTHON) scripts/sdk/reference.py --go "$(GO)" --check
	$(PYTHON) scripts/sdk/sync_guides.py --check
