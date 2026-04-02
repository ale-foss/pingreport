# PingReport Makefile

.PHONY: all build test test-coverage fmt clean help run-example

# Default target
all: fmt test build

# Build the Windows executable
build:
	@echo "Building pingreport.exe..."
	@go build -o pingreport.exe ./cmd/pingreport
	@echo "Build complete: pingreport.exe"

# Run all tests
test:
	@echo "Running tests..."
	@go test ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	@go test -cover ./...
	@echo "Generating detailed coverage report..."
	@go test -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Format all Go code
fmt:
	@echo "Formatting code..."
	@go fmt ./...

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -f pingreport.exe coverage.out coverage.html
	@echo "Clean complete"

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	@go mod download

# Tidy dependencies
tidy:
	@echo "Tidying dependencies..."
	@go mod tidy

# Lint code (requires golangci-lint to be installed)
lint:
	@echo "Linting code..."
	@golangci-lint run

# Create a test log file for development
create-test-log:
	@echo "Creating test ping log..."
	@echo "[1640971234.123456] 64 bytes from 8.8.8.8: icmp_seq=1 ttl=64 time=12.6 ms" > test_ping.log
	@echo "[1640971235.223456] 64 bytes from 8.8.8.8: icmp_seq=2 ttl=64 time=13.1 ms" >> test_ping.log
	@echo "[1640971236.323456] no answer yet for icmp_seq=3" >> test_ping.log
	@echo "[1640971237.423456] 64 bytes from 8.8.8.8: icmp_seq=4 ttl=64 time=11.8 ms" >> test_ping.log
	@echo "[1640971238.523456] 64 bytes from 8.8.8.8: icmp_seq=7 ttl=64 time=14.2 ms" >> test_ping.log
	@echo "[1640971239.623456] From 8.8.8.8 icmp_seq=8 Destination Host Unreachable" >> test_ping.log
	@echo "[1640971240.723456] 64 bytes from 8.8.8.8: icmp_seq=9 ttl=64 time=10.5 ms" >> test_ping.log
	@echo "--- 8.8.8.8 ping statistics ---" >> test_ping.log
	@echo "9 packets transmitted, 6 received, 33%% packet loss, time 6000ms" >> test_ping.log
	@echo "Test log created: test_ping.log"

# Run example with test log
run-example: build create-test-log
	@echo "Running example with test log..."
	@./pingreport.exe --html test_report.html --csv test_data.csv test_ping.log
	@echo "Example complete. Check test_report.html and test_data.csv"

# Build for distribution (with optimizations)
build-release:
	@echo "Building release version..."
	@go build -ldflags="-s -w" -o pingreport.exe ./cmd/pingreport
	@echo "Release build complete: pingreport.exe"

# Benchmark tests
bench:
	@echo "Running benchmarks..."
	@go test -bench=. ./...

# Check for vulnerabilities
vuln-check:
	@echo "Checking for vulnerabilities..."
	@go list -json -deps ./... | nancy sleuth

# Initialize project (run once after cloning)
init: deps tidy
	@echo "Project initialized successfully"

# Show help
help:
	@echo "Available targets:"
	@echo "  build           Build the executable"
	@echo "  test            Run all tests"
	@echo "  test-coverage   Run tests with coverage report"
	@echo "  fmt             Format all Go code"
	@echo "  clean           Clean build artifacts"
	@echo "  deps            Download dependencies"
	@echo "  tidy            Tidy dependencies"
	@echo "  lint            Lint code (requires golangci-lint)"
	@echo "  create-test-log Create a sample ping log for testing"
	@echo "  run-example     Build and run with test data"
	@echo "  build-release   Build optimized release version"
	@echo "  bench           Run benchmark tests"
	@echo "  vuln-check      Check for vulnerabilities (requires nancy)"
	@echo "  init            Initialize project after cloning"
	@echo "  all             Format, test, and build (default)"
	@echo "  help            Show this help message"