# PROJECT KNOWLEDGE BASE

**Generated:** 2026-02-26
**Commit:** 7dc56e5
**Branch:** main

## OVERVIEW

Go CLI tool that identifies underutilized SLOs by analyzing error budget time series from cloud monitoring APIs, generating Excel reports. Currently GCP-only; Datadog planned.

## STRUCTURE

```
vigil/
├── main.go        # CLI entry, flag parsing, concurrent SLO processing, Excel report generation
├── client.go      # Vigil interface (cloud provider abstraction)
├── gcp/gcp.go     # GCP Cloud Monitoring implementation (services → SLOs → time series)
├── model/
│   ├── slo.go     # SLO + SLOData domain structs
│   └── vigil.go   # CloudProvider enum (gcp, datadog)
├── utils/
│   ├── calc.go    # GetMinAvgErrorBudget, IsPercentNegative
│   └── interface.go # ToInterfaceSlice (SLO slice conversion)
└── assets/        # README images (og.png, excel.png)
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Add CLI flags | `main.go:23-30` | Global `flag.*` vars |
| Change SLO detection logic | `main.go:111-149` (`processSLO`) | `flagBelowThreshold` + `flagNegative` determine inclusion |
| Add new cloud provider | Create `{provider}/` pkg implementing `Vigil` interface in `client.go` | Follow `gcp/gcp.go` pattern |
| Modify Excel output | `main.go:167-223` (`generateExcelReport`) | Uses `excelize/v2` |
| Change domain models | `model/slo.go` | `SLO.SLI` is `interface{}` (holds provider-specific proto) |
| Error budget calculations | `utils/calc.go` | Pure math, no side effects |

## CODE MAP

| Symbol | Type | Location | Role |
|--------|------|----------|------|
| `Vigil` | interface | `client.go:9` | Cloud provider contract: GetProvider, GetSLOs, GetErrorBudgetTimeSeries |
| `gcp.Client` | struct | `gcp/gcp.go:16` | GCP implementation with MonitoringClient + MetricClient |
| `processSLO` | func | `main.go:111` | Core logic: fetches time series, evaluates threshold + negative flags |
| `generateExcelReport` | func | `main.go:167` | Writes flagged SLOs to styled xlsx |
| `model.SLO` | struct | `model/slo.go:3` | Domain model; `SLI` field is `interface{}` cast to `*monitoringpb.ServiceLevelIndicator` in GCP |
| `model.SLOData` | struct | `model/slo.go:10` | Report row: Flag, SLO goal, queries, min/avg budget |

## CONVENTIONS

- **No Makefile/Dockerfile** — build with `go build` or `go install github.com/rluisr/vigil@main`
- **No tests** — codebase has zero test files
- **Strict linting** — `.golangci.yml` enables 54 linters including `exhaustruct`, `nakedret` (max-func-lines: 0), `nolintlint` (requires explanation + specific linter)
- **Blocked modules** — `github.com/golang/protobuf` → use `google.golang.org/protobuf`; `satori/go.uuid` and `gofrs/uuid` → use `google/uuid`
- **Concurrency** — `maxConcurrency = 16` with semaphore pattern for SLO processing
- **Error handling** — `log.Panicf` for fatal, `log.Printf` for warnings, `fmt.Errorf` with `%w` for wrapping
- **Commit style** — Conventional commits: `feat:`, `fix:`, `refactor:`, `docs:`, `ci:`

## ANTI-PATTERNS (THIS PROJECT)

- **No naked returns** — linter enforces `max-func-lines: 0`
- **No `//nolint` without explanation** — must specify linter name + reason
- **No deprecated proto library** — blocked by `gomodguard`
- **No `os.Setenv` in tests** — must use `t.Setenv()`
- **SLO.SLI is `interface{}`** — must type-assert to provider-specific proto in each cloud provider implementation

## UNIQUE STYLES

- `handleError(err, msg)` helper in `main.go` wraps `log.Fatalf` — used exclusively for Excel operations
- `setCellWithStyle` / `setCellValue` — thin wrappers over excelize; all Excel operations go through these
- `setColWidth` accepts `"B-E"` range format (custom parser at `main.go:264`)
- `warnMessages` + `warnMutex` — thread-safe warning accumulator for non-fatal SLO processing issues (e.g., "no data points found")

## COMMANDS

```bash
# Build
go build -o vigil .

# Install
go install github.com/rluisr/vigil@main

# Run
./vigil --cloud gcp --gcp-project PROJECT_ID --error-budget-threshold 0.99 --window 720h

# Lint
golangci-lint run
```

## NOTES

- Output file is always `slo_report.xlsx` in CWD (hardcoded)
- GCP auth via ADC (Application Default Credentials) — no explicit auth flags
- `model.SLO.SLI` stores raw `*monitoringpb.ServiceLevelIndicator` as `interface{}` — fragile if adding non-GCP providers
- The `utils/interface.go:ToInterfaceSlice` appears unused in current code
- `vigil` binary is committed to repo (`.gitignore` only ignores `*.xlsx`)
- CI only does auto-tagging — no lint/test pipeline
