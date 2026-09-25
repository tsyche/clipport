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

# Super-linter parity: golangci-lint/prettier/textlint/markdownlint/shfmt/actionlint, pinned (catch CI lint failures before push)
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
    # cache clean: a warm cache once hid a revive finding CI caught.
    "$dir/golangci-lint" cache clean
    "$dir/golangci-lint" run
    echo "== prettier 3.3.3 =="
    npx --yes prettier@3.3.3 --check '*.md' 'docs/**/*.md' '*.json' '.github/**/*.yml' '.github/**/*.json'
    echo "== markdownlint-cli 0.41.0 (.markdownlint.json) =="
    npx --yes markdownlint-cli@0.41.0 'docs/**/*.md' '*.md'
    echo "== textlint 14.2.0 + terminology (.textlintrc.json) =="
    mapfile -t files < <(find . -name '*.md' -not -path './.git/*' | sort)
    npx --yes --package=textlint@14.2.0 --package=textlint-rule-terminology \
        --package=textlint-filter-rule-comments textlint "${files[@]}"
    echo "== shfmt 3.10.0 (super-linter SHELL_SHFMT parity) =="
    unformatted="$(go run mvdan.cc/sh/v3/cmd/shfmt@v3.10.0 -l scripts)"
    if [ -n "$unformatted" ]; then
        echo "shfmt needs reformat:"
        echo "$unformatted"
        exit 1
    fi
    echo "== actionlint v1.7.12 (super-linter GITHUB_ACTIONS parity, config .github/actionlint.yml) =="
    # golangci-lint above pins GOTOOLCHAIN=go1.23; actionlint needs >=1.25.
    GOTOOLCHAIN=auto go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -config-file .github/actionlint.yml

# Validate documentation claims (agent-doc sync, just-recipe references, links, docs TL;DRs)
check-docs:
    ./scripts/check-docs.sh

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
