package main

import (
"flag"
"fmt"
"os"
"path/filepath"

"github.com/sqweek/dialog"

"pingreport/internal/fileset"
"pingreport/internal/parser"
"pingreport/internal/report"
"pingreport/internal/stats"
)

const (
version          = "1.0.0"
defaultPPS       = 375.0 // 1 / 0.00267 s (phone ping -i 0.00267)
defaultMaxPoints = 300
)

// Config holds command line configuration
type Config struct {
InputPath   string // folder containing PingResult_*.txt files
OutputPath  string
CSVPath     string
PPS         float64
MaxPoints   int
ShowHelp    bool
ShowVersion bool
}

func main() {
config, err := parseFlags()
if err != nil {
fmt.Fprintf(os.Stderr, "Error: %v\n", err)
os.Exit(1)
}

if config.ShowHelp {
showHelp()
return
}

if config.ShowVersion {
fmt.Printf("pingreport version %s\n", version)
return
}

// If no folder specified, open a folder dialog
if config.InputPath == "" {
fmt.Println("Loading...")
dirPath, err := promptForDirectory()
if err != nil {
fmt.Fprintf(os.Stderr, "Error selecting folder: %v\n", err)
os.Exit(1)
}
config.InputPath = dirPath
}

// Validate folder exists
info, err := os.Stat(config.InputPath)
if err != nil || !info.IsDir() {
fmt.Fprintf(os.Stderr, "Error: %q is not a valid directory\n", config.InputPath)
os.Exit(1)
}

// Default output: <folder>_report.html next to the folder
if config.OutputPath == "" {
config.OutputPath = filepath.Join(
filepath.Dir(config.InputPath),
filepath.Base(config.InputPath)+"_report.html",
)
}

if err := runAnalysis(config); err != nil {
fmt.Fprintf(os.Stderr, "Error: %v\n", err)
os.Exit(1)
}
}

// parseFlags parses command line arguments
func parseFlags() (*Config, error) {
config := &Config{}

flag.StringVar(&config.InputPath, "dir", "", "Folder containing PingResult_*.txt files")
flag.StringVar(&config.OutputPath, "html", "", "Output path for HTML report")
flag.StringVar(&config.CSVPath, "csv", "", "Output path for CSV export (optional)")
flag.Float64Var(&config.PPS, "pps", defaultPPS, "Packets per second for timestamp interpolation")
flag.IntVar(&config.MaxPoints, "max-points", defaultMaxPoints, "Maximum points per chart trace for LTTB downsampling")
flag.BoolVar(&config.ShowHelp, "help", false, "Show help message")
flag.BoolVar(&config.ShowHelp, "h", false, "Show help message")
flag.BoolVar(&config.ShowVersion, "version", false, "Show version information")
flag.BoolVar(&config.ShowVersion, "v", false, "Show version information")

flag.Parse()

// Positional arg is the folder path
if args := flag.Args(); len(args) > 0 && config.InputPath == "" {
config.InputPath = args[0]
}

if config.PPS <= 0 {
return nil, fmt.Errorf("pps must be positive, got %f", config.PPS)
}
if config.MaxPoints <= 0 {
return nil, fmt.Errorf("max-points must be positive, got %d", config.MaxPoints)
}

return config, nil
}

// promptForDirectory shows a directory dialog to select a folder.
func promptForDirectory() (string, error) {
dirPath, err := dialog.Directory().Title("Select folder containing PingResult_*.txt files").Browse()
if err != nil {
if err == dialog.ErrCancelled {
fmt.Println("Folder selection cancelled")
os.Exit(0)
}
return "", fmt.Errorf("failed to open directory dialog: %w", err)
}
return dirPath, nil
}

// runAnalysis performs the complete ping log analysis
func runAnalysis(config *Config) error {
paths, err := fileset.Discover(config.InputPath)
if err != nil {
return err
}
fmt.Printf("Found %d PingResult_*.txt file(s) in %s\n", len(paths), config.InputPath)
var fileSize int64
for i, p := range paths {
info, _ := os.Stat(p)
if info != nil {
fileSize += info.Size()
}
fmt.Printf("  [%d] %s\n", i+1, filepath.Base(p))
}
fmt.Println()

inputReader, err := fileset.NewMultiReader(paths)
if err != nil {
return err
}
defer inputReader.Close()

fmt.Printf("Analyzing ping log: %s\n", config.InputPath)
fmt.Printf("Using %.1f packets per second for interpolation\n", config.PPS)

p := parser.NewParser(config.PPS)
p.SetProgressCallback(func(linesProcessed int, bytesRead int64) {
percentage := float64(bytesRead) / float64(fileSize) * 100.0
fmt.Printf("\rParsing: %s lines (%.1f%%)...                    ", formatNumber(linesProcessed), percentage)
})

parseResult, err := p.Parse(inputReader)
if err != nil {
return fmt.Errorf("failed to parse ping log: %w", err)
}
fmt.Printf("\r%-60s\r", "") // Clear progress line

fmt.Printf("Parsed %d ping events\n", len(parseResult.Events))
if parseResult.ParseErrors > 0 {
fmt.Printf("Warning: %d parse errors encountered\n", parseResult.ParseErrors)
}

if len(parseResult.Events) == 0 {
return fmt.Errorf("no ping events found in the log file")
}

statistics := stats.ComputeStatisticsWithProgress(parseResult.Events, func(eventsProcessed int) {
fmt.Printf("\rComputing statistics: %s / %s events...           ", formatNumber(eventsProcessed), formatNumber(len(parseResult.Events)))
})
fmt.Printf("\r%-60s\r", "") // Clear progress line

printSummary(statistics)

fmt.Println("Generating HTML report...")
generator, err := report.NewGenerator()
if err != nil {
return fmt.Errorf("failed to create report generator: %w", err)
}

if err = generator.GenerateReport(
statistics,
config.InputPath,
config.OutputPath,
parseResult.ParseErrors,
config.MaxPoints,
true,
); err != nil {
return fmt.Errorf("failed to generate HTML report: %w", err)
}

if config.CSVPath != "" {
fmt.Println("Exporting CSV...")
if err := report.ExportCSV(statistics.Timeline, config.CSVPath); err != nil {
return fmt.Errorf("failed to export CSV: %w", err)
}
}

fmt.Println("Analysis complete!")
return nil
}

// formatNumber formats a number with thousand separators
func formatNumber(n int) string {
if n < 1000 {
return fmt.Sprintf("%d", n)
}
s := fmt.Sprintf("%d", n)
var result []byte
for i, c := range s {
if i > 0 && (len(s)-i)%3 == 0 {
result = append(result, ',')
}
result = append(result, byte(c))
}
return string(result)
}

// printSummary prints a summary of statistics to the console
func printSummary(statistics *stats.Statistics) {
fmt.Printf("\n=== PING ANALYSIS SUMMARY ===\n")
fmt.Printf("Total Packets:        %d\n", statistics.Summary.TotalPackets)
fmt.Printf("Received:             %d\n", statistics.Summary.ReceivedPackets)
fmt.Printf("Lost:                 %d\n", statistics.Summary.LostPackets)
fmt.Printf("Loss Rate:           %.2f%%\n", statistics.Summary.LossRate*100)
fmt.Printf("Max Consecutive Loss: %d packets\n", statistics.Summary.MaxConsecutiveLoss)
fmt.Printf("Max Loss Rate/Sec:   %.2f%%\n", statistics.Summary.MaxLossRatePerSec*100)

if statistics.Summary.ReceivedPackets > 0 {
fmt.Printf("\nLatency Statistics:\n")
fmt.Printf("  Min:                %.3f ms\n", statistics.Summary.RTTMinMs)
fmt.Printf("  Mean:               %.3f ms\n", statistics.Summary.RTTMeanMs)
fmt.Printf("  Max:                %.3f ms\n", statistics.Summary.RTTMaxMs)
fmt.Printf("  Std Dev:            %.3f ms\n", statistics.Summary.RTTStdMs)
}

if len(statistics.Timeline.TsSeconds) > 0 {
duration := statistics.Timeline.TsSeconds[len(statistics.Timeline.TsSeconds)-1] - statistics.Timeline.TsSeconds[0]
fmt.Printf("\nTest Duration:        %s\n", formatDuration(duration))
fmt.Printf("Timeline Data Points: %d seconds\n", len(statistics.Timeline.TsSeconds))
}

fmt.Printf("\n")
}

// formatDuration formats a duration in seconds to a human-readable compact string.
// Leading zero units are omitted; once a unit is shown all lower units are always shown.
func formatDuration(seconds int64) string {
days := seconds / 86400
hours := (seconds % 86400) / 3600
minutes := (seconds % 3600) / 60
secs := seconds % 60
switch {
case days > 0:
return fmt.Sprintf("%dd%02dh%02dm%02ds", days, hours, minutes, secs)
case hours > 0:
return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, secs)
case minutes > 0:
return fmt.Sprintf("%dm%02ds", minutes, secs)
default:
return fmt.Sprintf("%ds", secs)
}
}

// showHelp displays usage information
func showHelp() {
fmt.Printf(`pingreport v%s - Generate interactive HTML reports from Linux ping logs

USAGE:
    pingreport FOLDER [OPTIONS]
    pingreport -dir FOLDER [OPTIONS]
    pingreport              (opens folder selection dialog)

INPUT:
    FOLDER             Path to folder containing PingResult_*.txt files

OPTIONS:
    -dir PATH          Folder path (alternative to positional argument)
    --html PATH        Output path for HTML report (default: <folder>_report.html)
    --csv PATH         Export per-second data to CSV (optional)
    --pps FLOAT        Packets per second for timestamp interpolation (default: %.0f)
    --max-points INT   Maximum points per chart for downsampling (default: %d)
    -h, --help         Show this help message
    -v, --version      Show version information

EXAMPLES:
    pingreport C:\captures\session1
    pingreport -dir C:\captures\session1 --html session1_report.html
    pingreport C:\captures\session1 --csv data.csv

`, version, defaultPPS, defaultMaxPoints)
}
