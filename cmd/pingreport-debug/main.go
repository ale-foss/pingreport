// pingreport-debug is a development-only build of pingreport with verbose
// diagnostic logging. It logs every file-system operation (stat, open, read,
// write, mkdir) together with Windows error codes so access-right issues can
// be diagnosed without attaching a debugger.
//
// All output is shown on the console (stderr) in real time.
// At the end of the run a timestamped log file is written next to the HTML
// report: <report-folder>/pingreport-debug-YYYYMMDD-HHMMSS.log
// If the output path is never resolved (early failure), the log falls back to
// the current working directory.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sqweek/dialog"

	"pingreport/internal/fileset"
	"pingreport/internal/parser"
	"pingreport/internal/report"
	"pingreport/internal/stats"
)

const (
	version          = "1.0.0"
	defaultPPS       = 375.0
	defaultMaxPoints = 300
)

// Config holds command line configuration.
type Config struct {
	InputPath   string
	OutputPath  string
	CSVPath     string
	PPS         float64
	MaxPoints   int
	ShowHelp    bool
	ShowVersion bool
}

var (
	// dlog writes to stderr + an in-memory buffer simultaneously.
	dlog *log.Logger
	// logBuf accumulates everything so the full run can be saved to a file at the end.
	logBuf bytes.Buffer
	// resolvedOutputPath is set as soon as we know where the HTML report will go,
	// so writeLogFile can place the log file in the same directory.
	resolvedOutputPath string
)

func initLogger() {
	multi := io.MultiWriter(os.Stderr, &logBuf)
	dlog = log.New(multi, "[DBG] ", log.Ltime|log.Lmicroseconds)
}

// writeLogFile saves the accumulated log buffer next to the HTML report.
// Falls back to the current working directory if the output path was never resolved.
func writeLogFile() {
	var logDir string
	if resolvedOutputPath != "" {
		logDir = filepath.Dir(resolvedOutputPath)
	} else {
		logDir, _ = os.Getwd()
	}

	logFileName := filepath.Join(logDir,
		fmt.Sprintf("pingreport-debug-%s.log", time.Now().Format("20060102-150405")))

	if err := os.WriteFile(logFileName, logBuf.Bytes(), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "\nWARNING: Could not write debug log to %q: %v\n", logFileName, err)
		return
	}
	fmt.Fprintf(os.Stderr, "\n=== Debug log saved: %s ===\n", logFileName)
}

// exitNow saves the log file before exiting. Use instead of os.Exit everywhere
// in this binary — os.Exit bypasses deferred functions.
func exitNow(code int) {
	writeLogFile()
	os.Exit(code)
}

func main() {
	initLogger()

	logSystemInfo()

	config, err := parseFlags()
	if err != nil {
		dlog.Printf("ERROR parsing flags: %v", err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitNow(1)
	}

	if config.ShowHelp {
		showHelp()
		return
	}

	if config.ShowVersion {
		fmt.Printf("pingreport-debug version %s\n", version)
		return
	}

	// interactive is true when no -dir flag was given; enables Y/N retry on empty folder.
	interactive := config.InputPath == ""

	for {
		if config.InputPath == "" {
			fmt.Println("Loading...")
			dirPath, cancelled, err := promptForDirectory()
			if cancelled {
				dlog.Printf("Folder selection cancelled by user")
				exitNow(0)
			}
			if err != nil {
				dlog.Printf("ERROR selecting folder: %v", err)
				fmt.Fprintf(os.Stderr, "Error selecting folder: %v\n", err)
				exitNow(1)
			}
			config.InputPath = dirPath
			config.OutputPath = ""
		}

		dlog.Printf("Selected input path: %q", config.InputPath)

		info, err := os.Stat(config.InputPath)
		if err != nil || !info.IsDir() {
			dlog.Printf("ERROR: %q is not a valid directory — stat err: %v", config.InputPath, err)
			logSyscallError(err)
			fmt.Fprintf(os.Stderr, "Error: %q is not a valid directory\n", config.InputPath)
			exitNow(1)
		}
		dlog.Printf("Input directory stat OK: mode=%s modtime=%s", info.Mode(), info.ModTime().Format(time.RFC3339))

		if config.OutputPath == "" {
			config.OutputPath = filepath.Join(
				filepath.Dir(config.InputPath),
				filepath.Base(config.InputPath)+"_report.html",
			)
		}
		dlog.Printf("Output path: %q", config.OutputPath)

		// Expose the output path so writeLogFile knows where to save the log.
		resolvedOutputPath = config.OutputPath

		err = runAnalysis(config)
		if err == nil {
			break
		}

		dlog.Printf("runAnalysis returned error: %v", err)

		if errors.Is(err, fileset.ErrNoFiles) && interactive {
			fmt.Printf("Error: %v\n", err)
			fmt.Print("Select another folder? [Y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(answer)
			if strings.EqualFold(answer, "y") {
				config.InputPath = ""
				continue
			}
			exitNow(0)
		}

		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitNow(1)
	}

	writeLogFile()
}

// logSystemInfo logs environment details that help diagnose permission issues.
func logSystemInfo() {
	dlog.Printf("=== SYSTEM INFO ===")
	dlog.Printf("pingreport-debug v%s", version)
	dlog.Printf("OS/Arch: %s/%s | Go: %s", runtime.GOOS, runtime.GOARCH, runtime.Version())

	wd, _ := os.Getwd()
	dlog.Printf("Working directory: %q", wd)

	if u, err := user.Current(); err == nil {
		dlog.Printf("Current user: %s (uid=%s, gid=%s, home=%s)", u.Username, u.Uid, u.Gid, u.HomeDir)
	} else {
		dlog.Printf("Could not retrieve current user: %v", err)
	}

	tmpDir := os.TempDir()
	dlog.Printf("Temp directory: %q", tmpDir)
	probeWrite(tmpDir, "temp-dir write probe")

	dlog.Printf("===================")
}

// probeWrite tries to create and immediately remove a temp file in dir.
// This verifies write access without leaving artifacts.
func probeWrite(dir, label string) {
	tmp, err := os.CreateTemp(dir, "pingreport-probe-*.tmp")
	if err != nil {
		dlog.Printf("  WRITE PROBE FAILED [%s] in %q: %v", label, dir, err)
		logSyscallError(err)
		return
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name)
	dlog.Printf("  write probe OK [%s]: created and removed %q", label, name)
}

// checkDirAccess logs stat + ReadDir results for a directory.
func checkDirAccess(dir string) {
	dlog.Printf("--- Directory access check: %q ---", dir)

	info, err := os.Stat(dir)
	if err != nil {
		dlog.Printf("  os.Stat FAILED: %v", err)
		logSyscallError(err)
		return
	}
	dlog.Printf("  os.Stat OK: mode=%s isDir=%v modtime=%s", info.Mode(), info.IsDir(), info.ModTime().Format(time.RFC3339))

	entries, err := os.ReadDir(dir)
	if err != nil {
		dlog.Printf("  os.ReadDir FAILED: %v", err)
		logSyscallError(err)
		return
	}
	dlog.Printf("  os.ReadDir OK: %d entries", len(entries))
	for _, e := range entries {
		ei, statErr := e.Info()
		if statErr != nil {
			dlog.Printf("    %-40s  (DirEntry.Info error: %v)", e.Name(), statErr)
			continue
		}
		dlog.Printf("    %-40s  mode=%-12s  size=%d", e.Name(), ei.Mode(), ei.Size())
	}
}

// checkFileReadAccess probes read access to a single file.
func checkFileReadAccess(path string) {
	dlog.Printf("--- File read access check: %q ---", path)

	info, err := os.Stat(path)
	if err != nil {
		dlog.Printf("  os.Stat FAILED: %v", err)
		logSyscallError(err)
		return
	}
	dlog.Printf("  os.Stat OK: mode=%s size=%d modtime=%s", info.Mode(), info.Size(), info.ModTime().Format(time.RFC3339))

	f, err := os.Open(path)
	if err != nil {
		dlog.Printf("  os.Open FAILED: %v", err)
		logSyscallError(err)
		return
	}
	defer f.Close()
	dlog.Printf("  os.Open OK")

	buf := make([]byte, 128)
	n, readErr := f.Read(buf)
	if readErr != nil && readErr != io.EOF {
		dlog.Printf("  f.Read FAILED after %d bytes: %v", n, readErr)
		logSyscallError(readErr)
	} else {
		dlog.Printf("  f.Read OK: %d bytes (first line preview: %q)", n, strings.SplitN(string(buf[:n]), "\n", 2)[0])
	}
}

// checkOutputWriteAccess probes write access for the output HTML path.
// It creates and immediately removes a sentinel file; the real write happens later.
func checkOutputWriteAccess(path string) {
	dlog.Printf("--- Output write access check: %q ---", path)

	dir := filepath.Dir(path)
	info, statErr := os.Stat(dir)
	if statErr != nil {
		dlog.Printf("  Output directory %q does not yet exist (will attempt MkdirAll)", dir)
	} else {
		dlog.Printf("  Output directory stat: mode=%s isDir=%v", info.Mode(), info.IsDir())
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		dlog.Printf("  os.MkdirAll(%q) FAILED: %v", dir, err)
		logSyscallError(err)
		return
	}
	dlog.Printf("  os.MkdirAll(%q) OK", dir)

	// Test that we can create the output file (then remove it so the real run can create it).
	f, err := os.Create(path)
	if err != nil {
		dlog.Printf("  os.Create(%q) FAILED: %v", path, err)
		logSyscallError(err)
		return
	}
	f.Close()
	os.Remove(path)
	dlog.Printf("  os.Create write probe OK (test file removed)")

	probeWrite(dir, "output directory write probe")
}

// logSyscallError unwraps os.PathError / os.LinkError to print the raw Windows error code.
func logSyscallError(err error) {
	if err == nil {
		return
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		dlog.Printf("  └─ PathError: op=%q path=%q", pathErr.Op, pathErr.Path)
		if errno, ok := pathErr.Err.(syscall.Errno); ok {
			dlog.Printf("  └─ Windows error code: %d (0x%08X) — %s", uint32(errno), uint32(errno), errno.Error())
		}
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		dlog.Printf("  └─ LinkError: op=%q old=%q new=%q", linkErr.Op, linkErr.Old, linkErr.New)
		if errno, ok := linkErr.Err.(syscall.Errno); ok {
			dlog.Printf("  └─ Windows error code: %d (0x%08X) — %s", uint32(errno), uint32(errno), errno.Error())
		}
	}
}

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

	if args := flag.Args(); len(args) > 0 && config.InputPath == "" {
		config.InputPath = args[0]
	}

	dlog.Printf("Parsed flags: dir=%q html=%q csv=%q pps=%.1f max-points=%d",
		config.InputPath, config.OutputPath, config.CSVPath, config.PPS, config.MaxPoints)

	if config.PPS <= 0 {
		return nil, fmt.Errorf("pps must be positive, got %f", config.PPS)
	}
	if config.MaxPoints <= 0 {
		return nil, fmt.Errorf("max-points must be positive, got %d", config.MaxPoints)
	}

	return config, nil
}

func promptForDirectory() (path string, cancelled bool, err error) {
	dlog.Printf("Opening directory selection dialog...")
	dirPath, dlgErr := dialog.Directory().Title("Select folder containing PingResult_*.txt files").Browse()
	if dlgErr != nil {
		if dlgErr == dialog.ErrCancelled {
			return "", true, nil
		}
		return "", false, fmt.Errorf("failed to open directory dialog: %w", dlgErr)
	}
	dlog.Printf("User selected directory: %q", dirPath)
	return dirPath, false, nil
}

func runAnalysis(config *Config) error {
	dlog.Printf("=== runAnalysis START ===")

	checkDirAccess(config.InputPath)

	dlog.Printf("Calling fileset.Discover(%q)", config.InputPath)
	paths, err := fileset.Discover(config.InputPath)
	if err != nil {
		dlog.Printf("fileset.Discover FAILED: %v", err)
		logSyscallError(err)
		return err
	}
	dlog.Printf("fileset.Discover OK: %d file(s) found", len(paths))

	fmt.Printf("Found %d PingResult_*.txt file(s) in %s\n", len(paths), config.InputPath)
	var fileSize int64
	for i, p := range paths {
		checkFileReadAccess(p)
		info, _ := os.Stat(p)
		if info != nil {
			fileSize += info.Size()
		}
		fmt.Printf("  [%d] %s\n", i+1, filepath.Base(p))
	}
	fmt.Println()

	// Check write access before starting heavy CPU work so errors are reported early.
	checkOutputWriteAccess(config.OutputPath)

	dlog.Printf("Calling fileset.NewMultiReader for %d file(s), total size=%d bytes", len(paths), fileSize)
	inputReader, err := fileset.NewMultiReader(paths)
	if err != nil {
		dlog.Printf("fileset.NewMultiReader FAILED: %v", err)
		logSyscallError(err)
		return err
	}
	defer inputReader.Close()
	dlog.Printf("fileset.NewMultiReader OK")

	fmt.Printf("Analyzing ping log: %s\n", config.InputPath)
	fmt.Printf("Using %.1f packets per second for interpolation\n", config.PPS)

	p := parser.NewParser(config.PPS)
	p.SetProgressCallback(func(linesProcessed int, bytesRead int64) {
		percentage := float64(bytesRead) / float64(fileSize) * 100.0
		fmt.Printf("\rParsing: %s lines (%.1f%%)...                    ", formatNumber(linesProcessed), percentage)
	})

	dlog.Printf("Starting parser.Parse...")
	parseResult, err := p.Parse(inputReader)
	if err != nil {
		dlog.Printf("parser.Parse FAILED: %v", err)
		return fmt.Errorf("failed to parse ping log: %w", err)
	}
	fmt.Printf("\r%-60s\r", "")
	dlog.Printf("parser.Parse OK: %d events, %d parse errors", len(parseResult.Events), parseResult.ParseErrors)

	fmt.Printf("Parsed %d ping events\n", len(parseResult.Events))
	if parseResult.ParseErrors > 0 {
		fmt.Printf("Warning: %d parse errors encountered\n", parseResult.ParseErrors)
	}

	if len(parseResult.Events) == 0 {
		dlog.Printf("ERROR: no ping events found in the log")
		return fmt.Errorf("no ping events found in the log file")
	}

	dlog.Printf("Computing statistics for %d events...", len(parseResult.Events))
	statistics := stats.ComputeStatisticsWithProgress(parseResult.Events, func(eventsProcessed int) {
		fmt.Printf("\rComputing statistics: %s / %s events...           ", formatNumber(eventsProcessed), formatNumber(len(parseResult.Events)))
	})
	fmt.Printf("\r%-60s\r", "")
	dlog.Printf("Statistics computed OK")

	printSummary(statistics)

	dlog.Printf("Creating report generator...")
	fmt.Println("Generating HTML report...")
	generator, err := report.NewGenerator()
	if err != nil {
		dlog.Printf("report.NewGenerator FAILED: %v", err)
		return fmt.Errorf("failed to create report generator: %w", err)
	}
	dlog.Printf("report.NewGenerator OK")

	dlog.Printf("Calling generator.GenerateReport: output=%q maxPoints=%d", config.OutputPath, config.MaxPoints)
	if err = generator.GenerateReport(
		statistics,
		config.InputPath,
		config.OutputPath,
		parseResult.ParseErrors,
		config.MaxPoints,
		true,
	); err != nil {
		dlog.Printf("generator.GenerateReport FAILED: %v", err)
		logSyscallError(err)
		return fmt.Errorf("failed to generate HTML report: %w", err)
	}
	dlog.Printf("generator.GenerateReport OK")

	if config.CSVPath != "" {
		dlog.Printf("Exporting CSV to %q", config.CSVPath)
		fmt.Println("Exporting CSV...")
		if err := report.ExportCSV(statistics.Timeline, config.CSVPath); err != nil {
			dlog.Printf("report.ExportCSV FAILED: %v", err)
			logSyscallError(err)
			return fmt.Errorf("failed to export CSV: %w", err)
		}
		dlog.Printf("report.ExportCSV OK")
	}

	dlog.Printf("=== runAnalysis COMPLETE ===")
	fmt.Println("Analysis complete!")
	return nil
}

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

func showHelp() {
	fmt.Printf(`pingreport-debug v%s — diagnostic build of pingreport

USAGE:
    pingreport-debug FOLDER [OPTIONS]
    pingreport-debug -dir FOLDER [OPTIONS]
    pingreport-debug              (opens folder selection dialog)

This binary behaves identically to pingreport.exe but logs every file-system
operation to stderr AND to a timestamped log file written next to the report:
    <report-folder>/pingreport-debug-YYYYMMDD-HHMMSS.log

OPTIONS (same as pingreport):
    -dir PATH          Folder path
    --html PATH        Output path for HTML report
    --csv PATH         Export per-second data to CSV (optional)
    --pps FLOAT        Packets per second (default: %.0f)
    --max-points INT   Maximum chart points (default: %d)
    -h, --help         Show this message
    -v, --version      Show version

`, version, defaultPPS, defaultMaxPoints)
}
