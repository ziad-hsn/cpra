# Define variables
GO_CMD = go
GO_TAGS = ark_tiny
GO_TAGS_SLIM = ark_tiny,nodocker,noprofile
GO_BUILD_FLAGS = -v -tags $(GO_TAGS)
GO_SECURE_FLAGS = -buildmode=pie -ldflags="-s -w" -tags $(GO_TAGS)
GO_SLIM_FLAGS = -trimpath -ldflags="-s -w" -tags $(GO_TAGS_SLIM)
GO_SECSIM_FLAGS = -buildmode=pie -trimpath -ldflags="-s -w" -tags $(GO_TAGS_SLIM)
GO_TEST_FLAGS = -v -race -coverprofile=coverage.out

# The name of your application and where its main package is located
APP_NAME = cpra
MAIN_PACKAGE = .
BUILD_DIR = bin

# Automatically find all Go source files, excluding the vendor directory
SOURCE_FILES = $(shell find . -name "*.go" | grep -v "/vendor/")

# Declare phony targets to force execution every time
.PHONY: all build buildsec buildslim buildsecslim test testslim clean run install fmt tidy

# Default target
all: build

# Standard build target
# Builds the application with standard flags (full features).
build:
	go clean -cache
	@mkdir -p $(BUILD_DIR)
	@echo "Building $(APP_NAME) (full)..."
	$(GO_CMD) build $(GO_BUILD_FLAGS) -o $(BUILD_DIR)/$(APP_NAME) $(MAIN_PACKAGE)

# Secure build target
# Builds the application with PIE and stripped symbols.
buildsec:
	go clean -cache
	@mkdir -p $(BUILD_DIR)
	@echo "Building secure $(APP_NAME) (full)..."
	$(GO_CMD) build $(GO_SECURE_FLAGS) -o $(BUILD_DIR)/$(APP_NAME) $(MAIN_PACKAGE)

# Slim build target
# Builds a smaller binary without Docker interventions and pprof (~50% size reduction).
# Use for deployments that don't need Docker intervention support.
buildslim:
	go clean -cache
	@mkdir -p $(BUILD_DIR)
	@echo "Building $(APP_NAME) (slim: no docker, no pprof)..."
	$(GO_CMD) build $(GO_SLIM_FLAGS) -o $(BUILD_DIR)/$(APP_NAME)-slim $(MAIN_PACKAGE)
	@ls -lh $(BUILD_DIR)/$(APP_NAME)-slim

# Secure slim build target
# Combines PIE security with slim binary optimizations.
buildsecslim:
	go clean -cache
	@mkdir -p $(BUILD_DIR)
	@echo "Building secure $(APP_NAME) (slim: no docker, no pprof)..."
	$(GO_CMD) build $(GO_SECSIM_FLAGS) -o $(BUILD_DIR)/$(APP_NAME)-slim $(MAIN_PACKAGE)
	@ls -lh $(BUILD_DIR)/$(APP_NAME)-slim

# Test target
# Runs all tests with race detector and code coverage.
test:
	@echo "Running tests..."
	$(GO_CMD) test $(GO_TEST_FLAGS) ./...

# Slim test target
# Runs tests using the same slim build tags as `buildslim` (no Docker interventions, no pprof).
testslim:
	@echo "Running tests (slim: no docker, no pprof)..."
	$(GO_CMD) test -v -race -tags $(GO_TAGS_SLIM) -coverprofile=coverage.out ./...

# Tidy target
# Cleans up unused dependencies and adds missing ones.
tidy:
	@echo "Tidying module dependencies..."
	$(GO_CMD) mod tidy

# Format target
# Formats all Go source code.
fmt:
	@echo "Formatting code..."
	$(GO_CMD) fmt ./...

# Clean target
# Removes compiled binary, coverage file, and Go build cache.
clean:
	@echo "Cleaning up..."
	$(GO_CMD) clean
	@rm -rf $(BUILD_DIR) coverage.out

# Run target
# Builds and runs the standard binary.
run: build
	@echo "Running $(APP_NAME)..."
	./$(BUILD_DIR)/$(APP_NAME)

# Install target
# Builds and installs the application into GOBIN.
install: build
	@echo "Installing $(APP_NAME)..."
	$(GO_CMD) install $(MAIN_PACKAGE)
