# Contributing

Thanks for considering a contribution to Clipport. This is a small fork — keep changes focused and the single-file structure intact unless there's a strong reason to split it up.

## Setup

```sh
git clone https://github.com/tsyche/clipport.git
cd clipport
just build
```

## Workflow

```sh
just test   # go test -race ./...
just lint   # go vet ./...
```

Run both before opening a PR. See `CLAUDE.md`/`AGENTS.md` for architecture notes and key files.

## Branching

Branch off `main` (the default branch). Keep PRs scoped to one change — don't bundle unrelated fixes or refactors together.

## Code style

- No CGO — this stays a single static Go binary.
- Match the existing style in `clipport.go`: minimal abstraction, platform-specific logic behind `runtime.GOOS` switches.
- New tests go in `clipport_test.go`.

## Reporting bugs / requesting features

Use the [issue templates](.github/ISSUE_TEMPLATE/) — bug reports and feature requests have separate forms.
