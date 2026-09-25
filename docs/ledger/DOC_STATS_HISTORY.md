# Doc stats history

Append-only log of doc-bloat statistics (files, words, growth ratio, density) per `audit-docs`/`consolidate-docs` run, so trends stay comparable across runs.

| Date       | Skill            | Non-exempt files | Words | Over 600w | Doc/code 90d ratio | Density (w/kLOC) | Source glob |
| ---------- | ---------------- | ---------------: | ----: | --------: | -----------------: | ---------------: | ----------- |
| 2026-09-23 | audit-docs       |                7 |  2983 |         2 |              23.6% |          1473.09 | `*.go`      |
| 2026-09-23 | consolidate-docs |                7 |  3006 |         2 |              23.6% |          1484.44 | `*.go`      |
| 2026-09-24 | audit-docs       |                7 |  4660 |         2 |              20.4% |           997.86 | `*.go`      |
