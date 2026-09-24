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

# Fuzz MonitorSentClips for 60s (same shape as the CI fuzz job)
fuzz:
    go test -race -run=^$ -fuzz=FuzzMonitorSentClips -fuzztime=60s .

# Run go vet
lint:
    go vet ./...

# Auto-format source files
lintfix:
    gofmt -w .

# Reproduce super-linter locally (pinned to super-linter v7.1.0 tool versions)
# so GO/PRETTIER/textlint/markdownlint failures are caught before push.
lintci:
    #!/usr/bin/env bash
    set -euo pipefail
    ver="1.60.3"
    dir="$HOME/.cache/clipport-tools/golangci-lint-$ver"
    if [ ! -x "$dir/golangci-lint" ]; then
        mkdir -p "$dir"
        curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b "$dir" "v$ver"
    fi
    # golangci-lint 1.60.3 (built with go1.23) cannot read newer toolchains'
    # export data; force the toolchain CI effectively uses (go1.23.x).
    export GOTOOLCHAIN="${GOLANGCI_GOTOOLCHAIN:-go1.23.12}"
    echo "== golangci-lint $ver (.golangci.yml matches super-linter template, GOTOOLCHAIN=$GOTOOLCHAIN) =="
    "$dir/golangci-lint" run
    echo "== prettier 3.3.3 =="
    npx --yes prettier@3.3.3 --check '*.md' 'docs/**/*.md'
    echo "== markdownlint-cli 0.41.0 (.markdownlint.json) =="
    npx --yes markdownlint-cli@0.41.0 'docs/**/*.md' '*.md'
    echo "== textlint 14.2.0 + terminology (.textlintrc.json) =="
    mapfile -t files < <(find . -name '*.md' -not -path './.git/*' | sort)
    npx --yes --package=textlint@14.2.0 --package=textlint-rule-terminology \
        --package=textlint-filter-rule-comments textlint "${files[@]}"

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
