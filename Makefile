# Shelley Makefile

.PHONY: build build-custom build-linux-aarch64 build-linux-x86 test test-go test-e2e ui serve clean help templates demo exe-scroll exe-scroll-all

# Default target
all: build

# Download the pinned exe-scroll release for the current build target. The
# fetcher caches immutable, SHA-256-verified assets outside the worktree.
exe-scroll:
	@python3 scripts/fetch-exe-scroll.py --os "$${GOOS:-$$(go env GOOS)}" --arch "$${GOARCH:-$$(go env GOARCH)}"

# GoReleaser cross-builds all four targets from one checkout, so all matching
# embed files must be present before it starts compiling.
exe-scroll-all:
	@python3 scripts/fetch-exe-scroll.py --all --wait 900

# Build templates into tarballs
templates:
	@for dir in templates/*/; do \
		name=$$(basename "$$dir"); \
		tar -czf "templates/$$name.tar.gz" -C "templates/$$name" --exclude='.DS_Store' .; \
	done

# Build the UI and Go binary
build: exe-scroll ui templates
	@echo "Building Shelley..."
	go build -o bin/shelley ./cmd/shelley

# Build a customized Shelley binary (see the customizing-shelley skill).
# Stamps the release tag this branch diverged from (latest release tag at the
# merge-base with origin/main) and marks the build as customized, so the
# version dialog knows the build has diverged from mainline and offers
# rebase-style upgrades instead of binary self-updates.
build-custom: exe-scroll ui templates
	@set -e; \
	CANON="$$HOME/.config/shelley/shelley-customization"; \
	if [ "$$(pwd -P)" != "$$(cd "$$CANON" 2>/dev/null && pwd -P)" ]; then \
		echo "warning: building outside $$CANON; the version dialog's rebase-upgrade flow assumes that checkout" >&2; \
	fi; \
	git fetch --tags origin main; \
	BASE=$$(git merge-base HEAD origin/main); \
	TAG=$$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$$BASE") || { \
		echo "error: no release tag reachable from the merge-base with origin/main; is this a shallow clone? (need a full clone with tags)" >&2; \
		exit 1; \
	}; \
	SHA=$$(git rev-parse --short HEAD); \
	go build -ldflags "\
	  -X shelley.exe.dev/version.Version=$${TAG#v}-custom.$$SHA \
	  -X shelley.exe.dev/version.Tag=$$TAG \
	  -X shelley.exe.dev/version.Customized=true" \
	  -o bin/shelley ./cmd/shelley; \
	echo "Built bin/shelley: customized, based on $$TAG, HEAD $$SHA"

# Build for Linux (auto-detect architecture)
build-linux: ui templates
	@echo "Building Shelley for Linux..."
	@ARCH=$$(uname -m); \
	case $$ARCH in \
		x86_64) GOARCH=amd64 ;; \
		aarch64|arm64) GOARCH=arm64 ;; \
		*) echo "Unsupported architecture: $$ARCH" && exit 1 ;; \
	esac; \
	GOOS=linux GOARCH=$$GOARCH $(MAKE) exe-scroll; \
	GOOS=linux GOARCH=$$GOARCH go build -o bin/shelley-linux ./cmd/shelley

# Build for Linux ARM64
build-linux-aarch64: ui templates
	@echo "Building Shelley for Linux ARM64..."
	@GOOS=linux GOARCH=arm64 $(MAKE) exe-scroll
	GOOS=linux GOARCH=arm64 go build -o bin/shelley-linux-aarch64 ./cmd/shelley

# Build for Linux x86_64
build-linux-x86: ui templates
	@echo "Building Shelley for Linux x86_64..."
	@GOOS=linux GOARCH=amd64 $(MAKE) exe-scroll
	GOOS=linux GOARCH=amd64 go build -o bin/shelley-linux-x86 ./cmd/shelley

# Build UI
ui:
	@cd ui && pnpm install --frozen-lockfile --silent && pnpm run --silent build

# Run Go tests
test-go: exe-scroll ui
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
	go run ./cmd/shelley --predictable-only --db test.db serve

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

# Build and (re)start the demo server
demo:
	@./demo.py

# Show help
help:
	@echo "Shelley Build Commands:"
	@echo ""
	@echo "  build         Build UI, templates, and Go binary"
	@echo "  build-custom  Build a customized binary stamped as diverged from mainline"
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
	@echo "  demo          Build and (re)start the demo server"
	@echo "  help          Show this help"

