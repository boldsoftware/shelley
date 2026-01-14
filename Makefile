# Shelley Makefile

.PHONY: build build-linux-aarch64 build-linux-x86 test test-go test-e2e ui serve clean help templates

# Default target
all: build

# Build templates into tarballs
templates:
	@for dir in templates/*/; do \
		name=$$(basename "$$dir"); \
		tar -czf "templates/$$name.tar.gz" -C "templates/$$name" --exclude='.DS_Store' .; \
	done

# Build the UI and Go binary
build: ui templates
	@echo "Building Shelley..."
	go build -o bin/shelley ./cmd/shelley

# Build for Linux (auto-detect architecture)
build-linux: ui templates
	@echo "Building Shelley for Linux..."
	@ARCH=$$(uname -m); \
	case $$ARCH in \
		x86_64) GOARCH=amd64 ;; \
		aarch64|arm64) GOARCH=arm64 ;; \
		*) echo "Unsupported architecture: $$ARCH" && exit 1 ;; \
	esac; \
	GOOS=linux GOARCH=$$GOARCH go build -o bin/shelley-linux ./cmd/shelley

# Build for Linux ARM64
build-linux-aarch64: ui templates
	@echo "Building Shelley for Linux ARM64..."
	GOOS=linux GOARCH=arm64 go build -o bin/shelley-linux-aarch64 ./cmd/shelley

# Build for Linux x86_64
build-linux-x86: ui templates
	@echo "Building Shelley for Linux x86_64..."
	GOOS=linux GOARCH=amd64 go build -o bin/shelley-linux-x86 ./cmd/shelley

# Build UI
ui:
	@cd ui && pnpm install --frozen-lockfile --silent && pnpm run --silent build

# Run Go tests
test-go: ui
	@echo "Running Go tests..."
	go test -v ./...

# Run end-to-end tests
test-e2e: ui
	@echo "Running E2E tests..."
	cd ui && pnpm run test:e2e

# Run E2E tests in headed mode (with visible browser)
test-e2e-headed: ui
	@echo "Running E2E tests (headed)..."
	cd ui && pnpm run test:e2e:headed

# Run E2E tests in UI mode
test-e2e-ui: ui
	@echo "Opening E2E test UI..."
	cd ui && pnpm run test:e2e:ui

# Run all tests
test: test-go test-e2e

# Serve Shelley with predictable model for testing
serve-test: ui
	@echo "Starting Shelley with predictable model..."
	go run ./cmd/shelley --model predictable --db test.db serve

# Serve Shelley normally
serve: ui
	@echo "Starting Shelley..."
	go run ./cmd/shelley serve

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf bin/
	rm -rf ui/dist/
	rm -rf ui/node_modules/
	rm -rf ui/test-results/
	rm -rf ui/playwright-report/
	rm -f *.db
	rm -f templates/*.tar.gz

# Show help
help:
	@echo "Shelley Build Commands:"
	@echo ""
	@echo "  build         Build UI, templates, and Go binary"
	@echo "  build-linux-aarch64  Build for Linux ARM64"
	@echo "  build-linux-x86      Build for Linux x86_64"
	@echo "  ui            Build UI only"
	@echo "  templates     Build template tarballs"
	@echo "  test          Run all tests (Go + E2E)"
	@echo "  test-go       Run Go tests only"
	@echo "  test-e2e      Run E2E tests (headless)"
	@echo "  test-e2e-headed  Run E2E tests (visible browser)"
	@echo "  test-e2e-ui   Open E2E test UI"
	@echo "  serve         Start Shelley server"
	@echo "  serve-test    Start Shelley with predictable model"
	@echo "  clean         Clean build artifacts"
	@echo "  help          Show this help"

