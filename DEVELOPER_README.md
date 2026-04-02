# PingReport Developer README

> Purpose: give a developer or AI agent enough accurate context to modify this project without first reading every source file.

## Project Snapshot

PingReport is a Go 1.17+ Windows CLI that reads a folder of split Linux ping logs named `PingResult_*.txt`, parses them into packet events, computes statistics, and generates a self-contained HTML report with embedded Plotly.js.

Current implementation model:

- Input is a folder, not a single file.
- The CLI discovers all `PingResult_*.txt` files in that folder and reads them as one logical stream.
- The generated HTML is fully offline and embeds its JS assets into the output.
- The active production template is `internal/report/templates/report.tmpl.html`.
- This public snapshot intentionally omits tests, sample data, and other dev-only helper files.

## Directory Map

```text
pingreport/
├── cmd/pingreport/
│   └── main.go                      Entry point, flags, pipeline orchestration
├── internal/
│   ├── fileset/
│   │   └── fileset.go              Folder discovery and chained reader
│   ├── parser/
│   │   └── parser.go               Streaming ping parser
│   ├── report/
│   │   ├── report.go               HTML/CSV generation and browser launch
│   │   ├── assets/
│   │   │   └── plotly.min.js       Embedded Plotly bundle
│   │   └── templates/
│   │       └── report.tmpl.html    Active production template
│   └── stats/
│       └── stats.go                Single-pass statistics and histograms
├── Makefile
├── README.md                       End-user README
├── DEVELOPER_README.md             This document
├── go.mod
└── pingreport.exe                  Built Windows executable distributed to users
```

Go module name: `pingreport`

## Pipeline Overview

```text
Folder containing PingResult_*.txt files
    -> fileset.Discover
    -> fileset.NewMultiReader
    -> parser.NewParser(...).Parse(...)
    -> stats.ComputeStatisticsWithProgress(...)
    -> report.NewGenerator().GenerateReport(...)
    -> optional report.ExportCSV(...)
    -> opens HTML via rundll32 on Windows
```

High-level separation of concerns:

- `fileset` knows how to find and order input files.
- `parser` turns ping text into `[]PingEvent`.
- `stats` converts events into summary, timeline, and histograms.
- `report` renders the embedded HTML template and optional CSV.
- `cmd/pingreport` wires the pipeline together.

## Current CLI Behavior

The current binary is folder-oriented.

Accepted usage:

```powershell
.\pingreport.exe C:\captures\session1
.\pingreport.exe -dir C:\captures\session1 --html session1_report.html
.\pingreport.exe -dir C:\captures\session1 --csv session1.csv
.\pingreport.exe
```

Behavior details:

- A positional argument, if present, is interpreted as a folder path.
- If no folder is provided, a Windows folder picker opens.
- The CLI rejects non-directory input.
- Default HTML output path is derived in `main.go` as `<parent>/<folder>_report.html`.
- CSV is only written when `-csv` is provided.

Supported flags:

| Flag | Meaning |
|---|---|
| `-dir PATH` | Folder containing `PingResult_*.txt` files |
| `-html PATH` | Output HTML path |
| `-csv PATH` | Output CSV path |
| `-pps FLOAT` | Packets per second used for timestamp interpolation |
| `-max-points INT` | Maximum chart points before JS-side downsampling |
| `-h`, `-help` | Show help |
| `-v`, `-version` | Show version |

## Input Model

The project assumes a folder containing one or more files whose names match this broad pattern:

```text
PingResult_<sortable-suffix>.txt
```

Important nuance:

- `fileset.Discover` does not validate a strict timestamp schema.
- It accepts any filename that starts with `PingResult_` and ends with `.txt`.
- Ordering is lexicographic on the suffix between `PingResult_` and `.txt`.
- In practice, filenames should remain zero-padded and sortable, for example `PingResult_20260325_054746.txt`.

## Core Types

### Parser types

```go
type PingEvent struct {
    Timestamp  float64
    SeqNumber  int
    RTT        float64
    IsLoss     bool
    IsExplicit bool
}

type ParseResult struct {
    Events      []PingEvent
    ParseErrors int
    FirstSeq    int
    LastSeq     int
}
```

`PingEvent` is the core handoff type between parser and stats.

### Stats types

`stats.Statistics` is the top-level object passed into report generation. It contains:

- `Summary`
- `Timeline`
- `Histograms`

Key timeline fields:

- `TsSeconds`
- `LossPerSec`
- `ConsecutiveLossAtSecEnd`
- `ConsecutiveLossMaxPerSec`
- `RTTMeanPerSec`
- `RTTMinPerSec`
- `RTTMaxPerSec`

Important distinction:

- `ConsecutiveLossAtSecEnd` is the streak length at the end of that second.
- `ConsecutiveLossMaxPerSec` is the maximum streak reached at any point within that second.
- The active streak chart uses `ConsecutiveLossMaxPerSec`.

## Package Notes

### `cmd/pingreport`

Responsibilities:

- Parse flags.
- Prompt for a folder if none is supplied.
- Discover `PingResult_*.txt` files.
- Parse, compute statistics, generate report, optionally export CSV.
- Print progress and summary information.

Key implementation facts:

- Input validation requires a directory.
- Progress is reported during parsing and statistics calculation.
- Default output HTML path is computed in `main.go`, not through `report.DetermineOutputPath`.

### `internal/fileset`

Responsibilities:

- Scan a directory for matching input files.
- Sort them by suffix.
- Return a single `io.ReadCloser` that streams all files in sequence.

Things to know:

- `Discover` ignores subdirectories and unrelated files.
- `NewMultiReader` opens all files and closes them together via a custom read closer.

### `internal/parser`

Responsibilities:

- Parse timestamped and non-timestamped ping output.
- Recognize replies, explicit timeouts, and destination unreachable lines.
- Synthesize loss events when sequence gaps are detected.
- Handle `icmp_seq` wrap-around at `65535 -> 0`.
- Skip ping summary blocks between file segments.

Primary supported timestamped lines:

```text
[1640971234.123456] 64 bytes from 8.8.8.8: icmp_seq=3205 ttl=64 time=12.6 ms
[1640971235.234567] no answer yet for icmp_seq=3206
[1640971235.345678] From 8.8.8.8 icmp_seq=3207 Destination Host Unreachable
```

Important parser behavior:

1. Explicit losses come from timeout or unreachable lines.
2. Implicit losses come from sequence gaps.
3. Summary blocks are treated as delimiters only.
4. The parser does not inject additional “in-flight” losses from `transmitted - received` summary lines.
5. Entering a summary block resets `lastSeqSeen`, which prevents false gap injection between adjacent files in folder mode.

Timestamp interpolation rules for synthesized gap events:

- If previous and next timestamps are known, use linear interpolation.
- If only previous is known, extrapolate forward using PPS.
- If only next is known, extrapolate backward using PPS.
- If neither is known, fall back to `missingSeq / PPS`.

### `internal/stats`

Responsibilities:

- Compute packet counts and loss metrics.
- Compute global RTT stats using Welford’s online algorithm.
- Build per-second timeline arrays.
- Build loss-streak and RTT histograms.
- Marshal NaN-containing fields safely to JSON.

Important implementation details:

- RTT values for loss events are stored as `math.NaN()`.
- `Summary.MarshalJSON` and `Timeline.MarshalJSON` convert NaN to JSON `null`.
- `TotalDurationSeconds` is based on bucket timestamps when available.
- `MaxLossRatePerSec` is computed from the worst per-second loss count divided by the maximum observed packets-per-second bucket.

### `internal/report`

Responsibilities:

- Embed the active HTML template and Plotly asset.
- Convert computed statistics into JSON for the template.
- Write the final HTML report.
- Optionally open the report in the default browser.
- Export CSV from the timeline.

Embedded assets:

- `assets/plotly.min.js`
- `templates/report.tmpl.html`

Template data flow:

1. `GenerateReport` builds a `ReportData` struct.
2. It JSON-marshals that struct.
3. The template injects it as:

```html
<script>
const reportData = JSON.parse({{.Data}});
const maxPoints = {{.MaxPoints}};
</script>
```

4. Client-side JavaScript reads `reportData` to build the charts.

Important report facts:

- The active template is `internal/report/templates/report.tmpl.html`.
- The streak chart uses `reportData.timeline.consecutive_loss_max_per_sec`.
- The HTML is self-contained and works offline.
- Browser opening is Windows-specific via `rundll32 url.dll,FileProtocolHandler`.

## Active Report Contents

The current report renders:

- KPI cards for packet totals, loss metrics, duration, and latency.
- Lost packets per second chart.
- Consecutive loss streak chart.
- Consecutive loss run length distribution chart.
- Latency over time chart.
- Latency distribution chart.

The production template also includes client-side aggregation and downsampling logic for large datasets.

## Build, Test, Run

Common commands:

```powershell
make build
make tidy
make lint
make clean
make help
```

Useful direct commands:

```powershell
go build ./cmd/pingreport
go test ./...
.\pingreport.exe C:\captures\session1
.\pingreport.exe -dir C:\captures\session1 --csv session1.csv
```

Notes:

- `make clean` uses `rm -f`, which assumes a Unix-like shell environment even on Windows.
- A rebuild from this public snapshot succeeds with `go build ./cmd/pingreport`.
- The public repo does not include sample input folders, tests, or dev mirrors.

## Extending the Project

### Add a new input format

1. Add a new regex field to `parser.Parser`.
2. Compile it in `NewParser`.
3. Match it in `parseEvent` or `parseNonTimestampedEvent`.
4. Validate with `go test ./...` and any private fixtures you keep outside the public repo.

### Add a new metric

1. Extend `stats.Summary`, `stats.Timeline`, or `stats.Histograms`.
2. Populate the field in `ComputeStatisticsWithProgress` or a helper.
3. Add custom JSON handling if the new field can contain NaN.
4. Update tests.

### Add a new chart

1. Expose the data in `stats.Statistics`.
2. Ensure it serializes correctly.
3. Add a target container in `internal/report/templates/report.tmpl.html`.
4. Read the value from `reportData` in the template JavaScript.
5. Add a report test if the change alters required HTML output.

### Add a new CLI flag

1. Extend `Config` in `cmd/pingreport/main.go`.
2. Register the flag in `parseFlags`.
3. Thread it through `runAnalysis`.
4. Update help text and both READMEs if the flag is user-facing.

## Common Gotchas

- Do not assume the input is a single ping file.
- Do not rely on ping summary footer counts to infer additional losses; the parser intentionally avoids that.
- Do not use raw `float64` JSON marshaling for NaN-bearing fields.
- Do not assume `report.DetermineOutputPath` represents current CLI output behavior; `main.go` computes the default HTML path itself.
- Do not expect tests, examples, or branding assets to be present in this public snapshot.

## Debugging Pointers

If packet counts look wrong:

- Inspect gap detection around `lastSeqSeen` in the parser.
- Check whether a summary block boundary is resetting state as intended.
- Verify the input filenames are sorted correctly for folder mode.

If charts are blank:

- Confirm the generated HTML contains `const reportData = JSON.parse(...)`.
- Check for NaN-to-null handling in stats JSON marshaling.
- Verify the template change was made in `internal/report/templates/report.tmpl.html`.

If the streak chart looks unexpected:

- Remember it uses max streak reached within each second, not only the streak at second end.

## Recommended Read Order

For a quick codebase pass, read in this order:

1. `cmd/pingreport/main.go`
2. `internal/fileset/fileset.go`
3. `internal/parser/parser.go`
4. `internal/stats/stats.go`
5. `internal/report/report.go`
6. `internal/report/templates/report.tmpl.html`

## License

Project license: MIT.

Bundled third-party components called out in the repo:

- Plotly.js v1.58.5
- sqweek/dialog
- TheTitanrain/w32
