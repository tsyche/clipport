# List available recipes
default:
    just --list

# Download dependencies
setup:
    go mod download

# Build the binary
build:
    go build -o clipport .

# Run all tests
test:
    go test -race ./...

# Run go vet
lint:
    go vet ./...

# Auto-format source files
lintfix:
    gofmt -w .

# Remove built binary
clean:
    rm -f clipport

# Full reset: clean and rebuild
fresh: clean build

# Install binary to /usr/local/bin
install: build
    mv clipport /usr/local/bin/clipport

# Sync CLAUDE.md and AGENTS.md (copies newer file to the other)
sync-docs:
    #!/usr/bin/env bash
    if [ "CLAUDE.md" -nt "AGENTS.md" ]; then
        cp CLAUDE.md AGENTS.md
        echo "Synced CLAUDE.md -> AGENTS.md"
    else
        cp AGENTS.md CLAUDE.md
        echo "Synced AGENTS.md -> CLAUDE.md"
    fi
