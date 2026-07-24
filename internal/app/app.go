// Package app contains the complete application logic for pingreport.
// Both cmd/pingreport and cmd/pingreport-debug call Main here; the only
// difference is whether debug mode is forced on.
package app

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sqweek/dialog"

	"pingreport/internal/fileset"
	"pingreport/internal/parser"
	"pingreport/internal/report"
	"pingreport/internal/stats"
	"pingreport/internal/winui"
)

const (
	Version          = "1.0.0"
	defaultPPS       = 375.0
	defaultMaxPoints = 300
)

// Config holds the resolved command-line configuration.
type Config struct {
	InputPath   string
	OutputPath  string
	CSVPath     string
	PPS         float64
	MaxPoints   int
	ShowHelp    bool
	ShowVersion bool
	DebugMode   bool
}

// Main is the single entry point shared by both binaries.
// forceDebug enables debug mode regardless of whether --debug was passed.
func Main(forceDebug bool) {
	config, err := parseFlags(forceDebug)
	if err != nil {
		// Too early for a proper die() � dbg not created yet.
		fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
		fmt.Fprint(os.Stderr, "Press Enter to close...")
		bufio.NewReader(os.Stdin).ReadString('\n')
		os.Exit(1)
	}

	if config.ShowHelp {
		showHelp(forceDebug)
		return
	}
	if config.ShowVersion {
		fmt.Printf("pingreport version %s\n", Version)
		return
	}

	// dbg is always non-nil. In non-verbose mode it collects log lines silently
	// in a buffer so they can be written to a log file if the run fails.
	dbg := newDebugState(config.DebugMode)

	// die writes the log to Downloads, prints the error + log location to the
	// console, then pauses so the user can read the message before the window
	// closes (important when launched by double-click from Explorer).
	die := func(fatalErr error) {
		dbg.log("FATAL: %v", fatalErr)
		logPath := dbg.writeLogFile()
		fmt.Fprintf(os.Stderr, "\nError: %v\n", fatalErr)
		if logPath != "" {
			fmt.Fprintf(os.Stderr, "Diagnostic log saved to:\n  %s\n", logPath)
		} else {
			fmt.Fprintln(os.Stderr, "Could not save diagnostic log � check the console output above.")
		}
		fmt.Fprint(os.Stderr, "\nPress Enter to close...")
		bufio.NewReader(os.Stdin).ReadString('\n')
		os.Exit(1)
	}

	if config.DebugMode {
		dbg.logSystemInfo()
		dbg.log("Parsed flags: dir=%q html=%q csv=%q pps=%.1f max-points=%d debug=%v",
			config.InputPath, config.OutputPath, config.CSVPath,
			config.PPS, config.MaxPoints, config.DebugMode)
	}

	interactive := config.InputPath == ""

mainLoop:
	for {
		if config.InputPath == "" {
			fmt.Println("Loading...")
			dirPath, cancelled, dlgErr := promptForDirectory(dbg)
			if cancelled {
				dbg.log("Folder selection cancelled by user")
				os.Exit(0)
			}
			if dlgErr != nil {
				die(dlgErr)
				return
			}
			config.InputPath = dirPath
			config.OutputPath = ""
		}

		dbg.log("Selected input path: %q", config.InputPath)

		info, statErr := os.Stat(config.InputPath)
		if statErr != nil || !info.IsDir() {
			dbg.log("ERROR: %q is not a valid directory � stat err: %v", config.InputPath, statErr)
			dbg.logSyscallError(statErr)
			die(fmt.Errorf("%q is not a valid directory", config.InputPath))
			return
		}
		dbg.log("Input directory stat OK: mode=%s modtime=%s",
			info.Mode(), info.ModTime().Format("2006-01-02T15:04:05Z07:00"))

		var (
			fallbackMsg         string
			isUserPickedOutput  bool
			reportName          string
			outputFallbackLevel int // 0=default path, 1=downloads, 2=user-picked
		)
		if config.OutputPath == "" {
			reportName = filepath.Base(config.InputPath) + "_report.html"
			defaultOutput := filepath.Join(filepath.Dir(config.InputPath), reportName)
			defaultDir := filepath.Dir(defaultOutput)

			dbg.log("Resolving output path. Default would be: %q", defaultOutput)
			dbg.log("Checking write access for default output dir: %q", defaultDir)

			switch {
			case dbg.canWriteDirLogged(defaultDir, "default output dir"):
				dbg.log("-> Using default output path")
				config.OutputPath = defaultOutput
				outputFallbackLevel = 0
			case dbg.canWriteDirLogged(downloadsDir(), "Downloads"):
				dbg.log("-> Default dir not writable; falling back to Downloads: %q", downloadsDir())
				config.OutputPath = filepath.Join(downloadsDir(), reportName)
				fallbackMsg = fmt.Sprintf(
					"The selected folder is read-only.\n\nThe report has been saved to:\n%s",
					config.OutputPath,
				)
				outputFallbackLevel = 1
			default:
				dbg.log("-> Both default dir and Downloads are NOT writable; showing folder picker")
				outputFallbackLevel = 2
				isUserPickedOutput = true
				dialog.Message(
					"The selected folder and your Downloads folder are both read-only.\n\nPlease choose a folder where the report can be saved.",
				).Title("PingReport � Choose Output Location").Info()
				outDir, dirErr := winui.BrowseForFolder("Choose a folder to save the report")
				if dirErr != nil {
					if dirErr == winui.ErrCancelled {
						dbg.log("Output location selection cancelled by user")
						fmt.Println("Output location selection cancelled.")
						os.Exit(0)
					}
					die(fmt.Errorf("selecting output directory: %w", dirErr))
					return
				}
				config.OutputPath = filepath.Join(outDir, reportName)
				fallbackMsg = fmt.Sprintf("The report has been saved to:\n%s", config.OutputPath)
			}
		}

		dbg.log("Output path resolved to: %q", config.OutputPath)
		dbg.setOutputPath(config.OutputPath)

		err = runAnalysis(config, dbg)
		if err == nil {
			if fallbackMsg != "" {
				dialog.Message("%s", fallbackMsg).Title("PingReport � Report Location").Info()
			}
			break
		}

		dbg.log("runAnalysis returned error: %v", err)

		// canWriteDirLogged can be a false positive on OneDrive-synced directories:
		// a short-lived *.tmp probe is permitted, but creating a persistent .html
		// file is denied by corporate sync policy. If runAnalysis returned a
		// write-permission error for an auto-selected path, escalate the fallback
		// chain instead of hard-erroring.
		if !isUserPickedOutput && reportName != "" && isWritePermissionError(err) {
			dbg.log("Write-permission error on auto-selected path (level %d); escalating fallback chain",
				outputFallbackLevel)

			if outputFallbackLevel == 0 {
				dbg.log("Trying Downloads fallback: %q", downloadsDir())
				if dbg.canWriteDirLogged(downloadsDir(), "Downloads (escalation)") {
					config.OutputPath = filepath.Join(downloadsDir(), reportName)
					dbg.setOutputPath(config.OutputPath)
					fallbackMsg = fmt.Sprintf(
						"The selected folder is read-only.\n\nThe report has been saved to:\n%s",
						config.OutputPath,
					)
					dbg.log("Retrying runAnalysis with Downloads path: %q", config.OutputPath)
					if dlErr := runAnalysis(config, dbg); dlErr == nil {
						dbg.log("runAnalysis succeeded with Downloads path")
						dialog.Message("%s", fallbackMsg).Title("PingReport � Report Location").Info()
						break
					} else if !isWritePermissionError(dlErr) {
						die(dlErr)
						return
					}
					dbg.log("Downloads path also permission-denied; escalating to folder picker")
				}
			}

			// Both auto-selected paths failed; show folder picker.
			dbg.log("Showing folder picker after auto-path permission failures")
			isUserPickedOutput = true
			dialog.Message(
				"The selected folder and your Downloads folder are both read-only.\n\nPlease choose a folder where the report can be saved.",
			).Title("PingReport � Choose Output Location").Info()
			outDir, dirErr := winui.BrowseForFolder("Choose a folder to save the report")
			if dirErr != nil {
				if dirErr == winui.ErrCancelled {
					dbg.log("Output location selection cancelled by user")
					fmt.Println("Output location selection cancelled.")
					os.Exit(0)
				}
				die(fmt.Errorf("selecting output directory: %w", dirErr))
				return
			}
			config.OutputPath = filepath.Join(outDir, reportName)
			dbg.setOutputPath(config.OutputPath)
			fallbackMsg = fmt.Sprintf("The report has been saved to:\n%s", config.OutputPath)
			dbg.log("User picked output dir: %q -> %q", outDir, config.OutputPath)
			if pErr := runAnalysis(config, dbg); pErr == nil {
				dbg.log("runAnalysis succeeded with user-picked path")
				dialog.Message("%s", fallbackMsg).Title("PingReport � Report Location").Info()
				break
			} else {
				dbg.log("runAnalysis FAILED with user-picked path: %v", pErr)
				dbg.logSyscallError(pErr)
				err = pErr
			}
			// Fall through to the isUserPickedOutput retry loop below.
		}

		// Write-error retry: if the user-picked output dir is not writable,
		// warn and ask for a different folder, then retry.
		if isUserPickedOutput && !dbg.canWriteDirLogged(filepath.Dir(config.OutputPath), "user-picked output dir") {
			for {
				dialog.Message(
					"Writing to the selected folder was forbidden.\n\nPlease choose a different folder.",
				).Title("PingReport � Write Forbidden").Error()
				outDir, dirErr := winui.BrowseForFolder("Choose a folder to save the report")
				if dirErr != nil {
					if dirErr == winui.ErrCancelled {
						fmt.Println("Output location selection cancelled.")
						os.Exit(0)
					}
					die(fmt.Errorf("selecting output directory: %w", dirErr))
					return
				}
				config.OutputPath = filepath.Join(outDir, reportName)
				dbg.setOutputPath(config.OutputPath)
				fallbackMsg = fmt.Sprintf("The report has been saved to:\n%s", config.OutputPath)

				err = runAnalysis(config, dbg)
				if err == nil {
					dialog.Message("%s", fallbackMsg).Title("PingReport � Report Location").Info()
					break mainLoop
				}
				if !dbg.canWriteDirLogged(outDir, "user-picked output dir (retry)") {
					continue // still write-forbidden, keep asking
				}
				// Non-write error inside the retry loop: surface it
				die(err)
				return
			}
		}

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
			os.Exit(0)
		}

		die(err)
		return
	}

	// Success. In verbose/debug mode write the log for traceability.
	if dbg.verbose {
		if logPath := dbg.writeLogFile(); logPath != "" {
			fmt.Fprintf(os.Stderr, "\n=== Debug log saved: %s ===\n", logPath)
		}
	}
}

// parseFlags parses command-line arguments and returns the resolved Config.
// forceDebug sets DebugMode to true after parsing regardless of the --debug flag.
func parseFlags(forceDebug bool) (*Config, error) {
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
	flag.BoolVar(&config.DebugMode, "debug", false, "Enable verbose diagnostic logging and write a debug log file")

	flag.Parse()

	if args := flag.Args(); len(args) > 0 && config.InputPath == "" {
		config.InputPath = args[0]
	}
	if forceDebug {
		config.DebugMode = true
	}
	if config.PPS <= 0 {
		return nil, fmt.Errorf("pps must be positive, got %f", config.PPS)
	}
	if config.MaxPoints <= 0 {
		return nil, fmt.Errorf("max-points must be positive, got %d", config.MaxPoints)
	}
	return config, nil
}

// promptForDirectory shows a folder-picker dialog.
// Returns (path, cancelled, error).
func promptForDirectory(dbg *debugState) (string, bool, error) {
	dbg.log("Opening directory selection dialog...")
	dirPath, err := winui.BrowseForFolder("Select folder containing PingResult_*.txt files")
	if err != nil {
		if err == winui.ErrCancelled {
			return "", true, nil
		}
		return "", false, fmt.Errorf("failed to open directory dialog: %w", err)
	}
	dbg.log("User selected directory: %q", dirPath)
	return dirPath, false, nil
}

// runAnalysis is the core pipeline: discover -> parse -> stats -> report.
func runAnalysis(config *Config, dbg *debugState) error {
	dbg.log("=== runAnalysis START ===")

	dbg.checkDirAccess(config.InputPath)

	dbg.log("Calling fileset.Discover(%q)", config.InputPath)
	paths, err := fileset.Discover(config.InputPath)
	if err != nil {
		dbg.log("fileset.Discover FAILED: %v", err)
		dbg.logSyscallError(err)
		return err
	}
	dbg.log("fileset.Discover OK: %d file(s) found", len(paths))

	fmt.Printf("Found %d PingResult_*.txt file(s) in %s\n", len(paths), config.InputPath)
	var fileSize int64
	for i, p := range paths {
		dbg.checkFileReadAccess(p)
		info, _ := os.Stat(p)
		if info != nil {
			fileSize += info.Size()
		}
		fmt.Printf("  [%d] %s\n", i+1, filepath.Base(p))
	}
	fmt.Println()

	dbg.checkOutputWriteAccess(config.OutputPath)

	dbg.log("Calling fileset.NewMultiReader for %d file(s), total size=%d bytes", len(paths), fileSize)
	inputReader, err := fileset.NewMultiReader(paths)
	if err != nil {
		dbg.log("fileset.NewMultiReader FAILED: %v", err)
		dbg.logSyscallError(err)
		return err
	}
	defer inputReader.Close()
	dbg.log("fileset.NewMultiReader OK")

	fmt.Printf("Analyzing ping log: %s\n", config.InputPath)
	fmt.Printf("Using %.1f packets per second for interpolation\n", config.PPS)

	p := parser.NewParser(config.PPS)
	p.SetProgressCallback(func(linesProcessed int, bytesRead int64) {
		percentage := float64(bytesRead) / float64(fileSize) * 100.0
		fmt.Printf("\rParsing: %s lines (%.1f%%)...                    ",
			formatNumber(linesProcessed), percentage)
	})

	dbg.log("Starting parser.Parse...")
	parseResult, err := p.Parse(inputReader)
	if err != nil {
		dbg.log("parser.Parse FAILED: %v", err)
		return fmt.Errorf("failed to parse ping log: %w", err)
	}
	fmt.Printf("\r%-60s\r", "")
	dbg.log("parser.Parse OK: %d events, %d parse errors",
		len(parseResult.Events), parseResult.ParseErrors)

	fmt.Printf("Parsed %d ping events\n", len(parseResult.Events))
	if parseResult.ParseErrors > 0 {
		fmt.Printf("Warning: %d parse errors encountered\n", parseResult.ParseErrors)
	}
	if len(parseResult.Events) == 0 {
		dbg.log("ERROR: no ping events found in the log")
		return fmt.Errorf("no ping events found in the log file")
	}

	dbg.log("Computing statistics for %d events...", len(parseResult.Events))
	statistics := stats.ComputeStatisticsWithProgress(parseResult.Events, func(eventsProcessed int) {
		fmt.Printf("\rComputing statistics: %s / %s events...           ",
			formatNumber(eventsProcessed), formatNumber(len(parseResult.Events)))
	})
	fmt.Printf("\r%-60s\r", "")
	dbg.log("Statistics computed OK")

	printSummary(statistics)

	dbg.log("Creating report generator...")
	fmt.Println("Generating HTML report...")
	generator, err := report.NewGenerator()
	if err != nil {
		dbg.log("report.NewGenerator FAILED: %v", err)
		return fmt.Errorf("failed to create report generator: %w", err)
	}
	dbg.log("report.NewGenerator OK")

	dbg.log("Calling generator.GenerateReport: output=%q maxPoints=%d",
		config.OutputPath, config.MaxPoints)
	if err = generator.GenerateReport(
		statistics,
		config.InputPath,
		config.OutputPath,
		parseResult.ParseErrors,
		config.MaxPoints,
		true,
	); err != nil {
		dbg.log("generator.GenerateReport FAILED: %v", err)
		dbg.logSyscallError(err)
		return fmt.Errorf("failed to generate HTML report: %w", err)
	}
	dbg.log("generator.GenerateReport OK")

	if config.CSVPath != "" {
		dbg.log("Exporting CSV to %q", config.CSVPath)
		fmt.Println("Exporting CSV...")
		if err := report.ExportCSV(statistics.Timeline, config.CSVPath); err != nil {
			dbg.log("report.ExportCSV FAILED: %v", err)
			dbg.logSyscallError(err)
			return fmt.Errorf("failed to export CSV: %w", err)
		}
		dbg.log("report.ExportCSV OK")
	}

	dbg.log("=== runAnalysis COMPLETE ===")
	fmt.Println("Analysis complete!")
	return nil
}

// isWritePermissionError reports whether err is an OS-level write-permission
// denial (e.g. Windows "Access is denied.", error 5).
func isWritePermissionError(err error) bool {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return os.IsPermission(pathErr.Err)
	}
	return os.IsPermission(err)
}

// downloadsDir returns the path to the user's Downloads folder.
func downloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, "Downloads")
}

// formatNumber formats a number with thousand separators.
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

// formatDuration formats a duration in seconds to a human-readable string.
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

// printSummary prints a human-readable analysis summary to stdout.
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
		duration := statistics.Timeline.TsSeconds[len(statistics.Timeline.TsSeconds)-1] -
			statistics.Timeline.TsSeconds[0]
		fmt.Printf("\nTest Duration:        %s\n", formatDuration(duration))
		fmt.Printf("Timeline Data Points: %d seconds\n", len(statistics.Timeline.TsSeconds))
	}
	fmt.Printf("\n")
}

// showHelp displays usage information.
func showHelp(forceDebug bool) {
	if forceDebug {
		fmt.Printf(`pingreport-debug v%s � diagnostic build of pingreport

USAGE:
    pingreport-debug FOLDER [OPTIONS]
    pingreport-debug -dir FOLDER [OPTIONS]
    pingreport-debug              (opens folder selection dialog)

This binary behaves identically to pingreport.exe but always runs with --debug:
every file-system operation is logged to stderr and to a timestamped log file:
    <Downloads>/pingreport-debug-YYYYMMDD-HHMMSS.log

OPTIONS (same as pingreport):
    -dir PATH          Folder path
    --html PATH        Output path for HTML report
    --csv PATH         Export per-second data to CSV (optional)
    --pps FLOAT        Packets per second (default: %.0f)
    --max-points INT   Maximum chart points (default: %d)
    -h, --help         Show this message
    -v, --version      Show version

`, defaultPPS, defaultMaxPoints)
		return
	}

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
    --debug            Enable verbose diagnostic logging (writes a log file to Downloads)
    -h, --help         Show this help message
    -v, --version      Show version information

EXAMPLES:
    pingreport C:\captures\session1
    pingreport -dir C:\captures\session1 --html session1_report.html
    pingreport C:\captures\session1 --csv data.csv
    pingreport C:\captures\session1 --debug

`, Version, defaultPPS, defaultMaxPoints)
}