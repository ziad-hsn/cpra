# Define variables
GO_CMD = go
GO_BUILD_FLAGS = -v -tags ark_tiny
GO_SECURE_FLAGS = -buildmode=pie -ldflags="-s -w"
GO_TEST_FLAGS = -v -race -coverprofile=coverage.out

# The name of your application and where its main package is located
APP_NAME = cpra
MAIN_PACKAGE = .
BUILD_DIR = bin

# Automatically find all Go source files, excluding the vendor directory
SOURCE_FILES = $(shell find . -name "*.go" | grep -v "/vendor/")

# Declare phony targets to force execution every time
.PHONY: all build buildsec test clean run install fmt tidy

# Default target
all: build

# Standard build target
# Builds the application with standard flags.
build:
	@mkdir -p $(BUILD_DIR)
	@echo "Building $(APP_NAME)..."
	$(GO_CMD) build $(GO_BUILD_FLAGS) -o $(BUILD_DIR)/$(APP_NAME) $(MAIN_PACKAGE)

# Secure build target
# Builds the application with additional secure flags.
buildsec:
	@mkdir -p $(BUILD_DIR)
	@echo "Building secure $(APP_NAME)..."
	$(GO_CMD) build $(GO_SECURE_FLAGS) -o $(BUILD_DIR)/$(APP_NAME) $(MAIN_PACKAGE)

# Test target
# Runs all tests with race detector and code coverage.
test:
	@echo "Running tests..."
	$(GO_CMD) test $(GO_TEST_FLAGS) ./...

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
