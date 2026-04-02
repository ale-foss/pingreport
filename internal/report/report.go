package report

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	
	"pingreport/internal/stats"
)

// Embedded assets
//go:embed assets/plotly.min.js
var plotlyJS string

//go:embed templates/report.tmpl.html
var htmlTemplateContent string

// ReportData represents the data structure passed to the HTML template
type ReportData struct {
	GeneratedAt string        `json:"generated_at"`
	InputFile   string        `json:"input_file"`
	ParseErrors int           `json:"parse_errors"`
	Summary     stats.Summary `json:"summary"`
	Timeline    stats.Timeline `json:"timeline"`
	Histograms  stats.Histograms `json:"histograms"`
}

// TemplateData represents the complete data passed to the HTML template
type TemplateData struct {
	GeneratedAt string
	InputFile   string
	ParseErrors int
	Summary     stats.Summary
	Data        string // JSON-encoded ReportData
	PlotlyJS       template.JS
	LogoDataURI    template.URL
	MaxPoints      int
}

// Generator handles HTML report generation
type Generator struct {
	tmpl *template.Template
}

// NewGenerator creates a new report generator
func NewGenerator() (*Generator, error) {
	// Create template with helper functions
	tmpl := template.New("report").Funcs(template.FuncMap{
		"mul": func(a, b float64) float64 {
			return a * b
		},
		"formatDuration": func(seconds float64) string {
			total := int(seconds)
			days := total / 86400
			hours := (total % 86400) / 3600
			minutes := (total % 3600) / 60
			secs := total % 60
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
		},
		"formatNumber": func(n int) string {
			if n < 1000 {
				return fmt.Sprintf("%d", n)
			}
			s := fmt.Sprintf("%d", n)
			var result []byte
			for i, c := range s {
				if i > 0 && (len(s)-i)%3 == 0 {
					result = append(result, ' ')
				}
				result = append(result, byte(c))
			}
			return string(result)
		},
		"formatFloatAsInt": func(f float64) string {
			n := int(f)
			if n < 1000 {
				return fmt.Sprintf("%d", n)
			}
			s := fmt.Sprintf("%d", n)
			var result []byte
			for i, c := range s {
				if i > 0 && (len(s)-i)%3 == 0 {
					result = append(result, ' ')
				}
				result = append(result, byte(c))
			}
			return string(result)
		},
	})
	
	// Parse the embedded template
	var err error
	tmpl, err = tmpl.Parse(htmlTemplateContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}
	
	return &Generator{
		tmpl: tmpl,
	}, nil
}

// GenerateReport generates an HTML report and optionally opens it in the browser
func (g *Generator) GenerateReport(statistics *stats.Statistics, inputPath, outputPath string, parseErrors int, maxPoints int, openBrowser bool) error {
	// Create report data
	reportData := ReportData{
		GeneratedAt: time.Now().Format(time.RFC3339),
		InputFile:   filepath.Base(inputPath),
		ParseErrors: parseErrors,
		Summary:     statistics.Summary,
		Timeline:    statistics.Timeline,
		Histograms:  statistics.Histograms,
	}
	
	// Convert to JSON for JavaScript consumption
	jsonData, err := json.Marshal(reportData)
	if err != nil {
		return fmt.Errorf("failed to marshal report data: %w", err)
	}
	
	// No branded assets are embedded in the generated report.
	logoDataURI := ""
	
	// Prepare template data
	templateData := TemplateData{
		GeneratedAt:    reportData.GeneratedAt,
		InputFile:      reportData.InputFile,
		ParseErrors:    reportData.ParseErrors,
		Summary:        reportData.Summary,
		Data:           string(jsonData),
		PlotlyJS:       template.JS(plotlyJS),
		LogoDataURI:    template.URL(logoDataURI),
		MaxPoints:      maxPoints,
	}
	
	// Create output directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	
	// Create output file
	outputFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outputFile.Close()
	
	// Execute template
	if err := g.tmpl.Execute(outputFile, templateData); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}
	
	fmt.Printf("Report generated: %s\n", outputPath)
	
	// Open in browser if requested
	if openBrowser {
		if err := g.openBrowser(outputPath); err != nil {
			fmt.Printf("Warning: Could not open browser: %v\n", err)
		} else {
			fmt.Println("Report opened in default browser")
		}
	}
	
	return nil
}

// openBrowser opens the report in the default browser (Windows-specific implementation)
func (g *Generator) openBrowser(htmlPath string) error {
	// Convert to absolute path if needed
	absPath, err := filepath.Abs(htmlPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}
	
	// Use Windows rundll32 to open in default browser
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", absPath)
	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to execute rundll32: %w", err)
	}
	
	return nil
}

// DetermineOutputPath determines the output path based on input and options
func DetermineOutputPath(inputPath, outputFlag string) string {
	if outputFlag != "" {
		return outputFlag
	}
	
	// Default: place report.html next to input file
	dir := filepath.Dir(inputPath)
	base := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	return filepath.Join(dir, base+"_report.html")
}

// ExportCSV exports per-second aggregated data to CSV format
func ExportCSV(timeline stats.Timeline, outputPath string) error {
	if len(timeline.TsSeconds) == 0 {
		return fmt.Errorf("no timeline data to export")
	}
	
	// Create output file
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer file.Close()
	
	// Write CSV header
	header := "timestamp,loss_count,rtt_mean_ms,rtt_min_ms,rtt_max_ms,consecutive_loss_streak\n"
	if _, err := file.WriteString(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}
	
	// Write data rows
	for i, ts := range timeline.TsSeconds {
		var rttMean, rttMin, rttMax string = "N/A", "N/A", "N/A"
		
		// Handle NaN values
		if i < len(timeline.RTTMeanPerSec) && !math.IsNaN(timeline.RTTMeanPerSec[i]) {
			rttMean = fmt.Sprintf("%.3f", timeline.RTTMeanPerSec[i])
		}
		if i < len(timeline.RTTMinPerSec) && !math.IsNaN(timeline.RTTMinPerSec[i]) {
			rttMin = fmt.Sprintf("%.3f", timeline.RTTMinPerSec[i])
		}
		if i < len(timeline.RTTMaxPerSec) && !math.IsNaN(timeline.RTTMaxPerSec[i]) {
			rttMax = fmt.Sprintf("%.3f", timeline.RTTMaxPerSec[i])
		}
		
		lossCount := 0
		if i < len(timeline.LossPerSec) {
			lossCount = timeline.LossPerSec[i]
		}
		
		streakEnd := 0
		if i < len(timeline.ConsecutiveLossAtSecEnd) {
			streakEnd = timeline.ConsecutiveLossAtSecEnd[i]
		}
		
		line := fmt.Sprintf("%d,%d,%s,%s,%s,%d\n",
			ts, lossCount, rttMean, rttMin, rttMax, streakEnd)
		
		if _, err := file.WriteString(line); err != nil {
			return fmt.Errorf("failed to write CSV data: %w", err)
		}
	}
	
	fmt.Printf("CSV exported: %s\n", outputPath)
	return nil
}

