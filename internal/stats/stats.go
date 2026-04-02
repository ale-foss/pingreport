package stats

import (
	"encoding/json"
	"math"
	"sort"
	
	"pingreport/internal/parser"
)

const (
	// defaultHistogramBins is the default number of bins for RTT histogram
	defaultHistogramBins = 80
)

// Summary contains overall statistics
type Summary struct {
	TotalPackets         int     `json:"total_packets"`
	ReceivedPackets      int     `json:"received_packets"`
	LostPackets          int     `json:"lost_packets"`
	LossRate             float64 `json:"loss_rate"`
	MaxConsecutiveLoss   int     `json:"max_consecutive_loss"`
	MaxLossRatePerSec    float64 `json:"max_loss_rate_per_sec"`
	MaxLostPerSec        int     `json:"max_lost_per_sec"`
	TotalDurationSeconds float64 `json:"total_duration_seconds"`
	RTTMinMs             float64 `json:"rtt_min_ms"`
	RTTMeanMs            float64 `json:"rtt_mean_ms"`
	RTTStdMs             float64 `json:"rtt_std_ms"`
	RTTMaxMs             float64 `json:"rtt_max_ms"`
}

// MarshalJSON implements custom JSON marshaling for Summary to handle NaN values
func (s Summary) MarshalJSON() ([]byte, error) {
	// Helper function to convert NaN to null
	nullIfNaN := func(v float64) interface{} {
		if math.IsNaN(v) {
			return nil
		}
		return v
	}
	
	temp := struct {
		TotalPackets         int         `json:"total_packets"`
		ReceivedPackets      int         `json:"received_packets"`
		LostPackets          int         `json:"lost_packets"`
		LossRate             float64     `json:"loss_rate"`
		MaxConsecutiveLoss   int         `json:"max_consecutive_loss"`
		MaxLossRatePerSec    float64     `json:"max_loss_rate_per_sec"`
		MaxLostPerSec        int         `json:"max_lost_per_sec"`
		TotalDurationSeconds float64     `json:"total_duration_seconds"`
		RTTMinMs             interface{} `json:"rtt_min_ms"`
		RTTMeanMs            interface{} `json:"rtt_mean_ms"`
		RTTStdMs             interface{} `json:"rtt_std_ms"`
		RTTMaxMs             interface{} `json:"rtt_max_ms"`
	}{
		TotalPackets:         s.TotalPackets,
		ReceivedPackets:      s.ReceivedPackets,
		LostPackets:          s.LostPackets,
		LossRate:             s.LossRate,
		MaxConsecutiveLoss:   s.MaxConsecutiveLoss,
		MaxLossRatePerSec:    s.MaxLossRatePerSec,
		MaxLostPerSec:        s.MaxLostPerSec,
		TotalDurationSeconds: s.TotalDurationSeconds,
		RTTMinMs:             nullIfNaN(s.RTTMinMs),
		RTTMeanMs:            nullIfNaN(s.RTTMeanMs),
		RTTStdMs:             nullIfNaN(s.RTTStdMs),
		RTTMaxMs:             nullIfNaN(s.RTTMaxMs),
	}
	
	return json.Marshal(temp)
}

// Timeline contains per-second aggregated data
type Timeline struct {
	TsSeconds                []int64   `json:"ts_sec"`
	LossPerSec               []int     `json:"loss_per_sec"`
	ConsecutiveLossAtSecEnd  []int     `json:"consecutive_loss_at_sec_end"`
	ConsecutiveLossMaxPerSec []int     `json:"consecutive_loss_max_per_sec"`
	RTTMeanPerSec            []float64 `json:"rtt_mean_per_sec"`
	RTTMinPerSec             []float64 `json:"rtt_min_per_sec"`
	RTTMaxPerSec             []float64 `json:"rtt_max_per_sec"`
}

// MarshalJSON implements custom JSON marshaling for Timeline to handle NaN values
func (t Timeline) MarshalJSON() ([]byte, error) {
	// Helper function to convert NaN to null in JSON
	convertNaN := func(values []float64) []interface{} {
		result := make([]interface{}, len(values))
		for i, v := range values {
			if math.IsNaN(v) {
				result[i] = nil
			} else {
				result[i] = v
			}
		}
		return result
	}
	
	// Create a temporary struct with interface{} slices for fields that might contain NaN
	temp := struct {
		TsSeconds                []int64       `json:"ts_sec"`
		LossPerSec               []int         `json:"loss_per_sec"`
		ConsecutiveLossAtSecEnd  []int         `json:"consecutive_loss_at_sec_end"`
		ConsecutiveLossMaxPerSec []int         `json:"consecutive_loss_max_per_sec"`
		RTTMeanPerSec            []interface{} `json:"rtt_mean_per_sec"`
		RTTMinPerSec             []interface{} `json:"rtt_min_per_sec"`
		RTTMaxPerSec             []interface{} `json:"rtt_max_per_sec"`
	}{
		TsSeconds:                t.TsSeconds,
		LossPerSec:               t.LossPerSec,
		ConsecutiveLossAtSecEnd:  t.ConsecutiveLossAtSecEnd,
		ConsecutiveLossMaxPerSec: t.ConsecutiveLossMaxPerSec,
		RTTMeanPerSec:            convertNaN(t.RTTMeanPerSec),
		RTTMinPerSec:             convertNaN(t.RTTMinPerSec),
		RTTMaxPerSec:             convertNaN(t.RTTMaxPerSec),
	}
	
	return json.Marshal(temp)
}

// RTTHistogram contains RTT distribution data
type RTTHistogram struct {
	BinEdgesMs []float64 `json:"bin_edges_ms"`
	Counts     []int     `json:"counts"`
}

// ConsecutiveLossHistogram contains consecutive loss run length distribution
type ConsecutiveLossHistogram struct {
	RunLengths []int `json:"run_lengths"`
	Counts     []int `json:"counts"`
}

// Histograms contains distribution data
type Histograms struct {
	RTT              RTTHistogram             `json:"rtt"`
	ConsecutiveLoss  ConsecutiveLossHistogram `json:"consecutive_loss"`
}

// Statistics contains all computed statistics
type Statistics struct {
	Summary     Summary     `json:"summary"`
	Timeline    Timeline    `json:"timeline"`
	Histograms  Histograms  `json:"histograms"`
}

// WelfordAccumulator implements Welford's online algorithm for mean and variance
type WelfordAccumulator struct {
	n    int64
	mean float64
	m2   float64
	min  float64
	max  float64
}

// NewWelfordAccumulator creates a new Welford accumulator
func NewWelfordAccumulator() *WelfordAccumulator {
	return &WelfordAccumulator{
		min: math.Inf(1),
		max: math.Inf(-1),
	}
}

// Add adds a value to the accumulator
func (w *WelfordAccumulator) Add(value float64) {
	w.n++
	delta := value - w.mean
	w.mean += delta / float64(w.n)
	delta2 := value - w.mean
	w.m2 += delta * delta2
	
	if value < w.min {
		w.min = value
	}
	if value > w.max {
		w.max = value
	}
}

// Mean returns the current mean
func (w *WelfordAccumulator) Mean() float64 {
	if w.n == 0 {
		return math.NaN()
	}
	return w.mean
}

// Variance returns the current variance
func (w *WelfordAccumulator) Variance() float64 {
	if w.n < 2 {
		return math.NaN()
	}
	return w.m2 / float64(w.n-1)
}

// StdDev returns the current standard deviation
func (w *WelfordAccumulator) StdDev() float64 {
	return math.Sqrt(w.Variance())
}

// Min returns the minimum value
func (w *WelfordAccumulator) Min() float64 {
	if w.n == 0 {
		return math.NaN()
	}
	return w.min
}

// Max returns the maximum value
func (w *WelfordAccumulator) Max() float64 {
	if w.n == 0 {
		return math.NaN()
	}
	return w.max
}

// Count returns the number of values added
func (w *WelfordAccumulator) Count() int64 {
	return w.n
}

// perSecondBucket represents data for one second
type perSecondBucket struct {
	lossCount          int
	rttAccumulator     *WelfordAccumulator
	consecutiveLossEnd int
	maxStreakInSec     int // maximum streak value reached during this second
}

// ProgressCallback is called periodically during computation to report progress
type ProgressCallback func(eventsProcessed int)

// ComputeStatistics computes all statistics from parsed ping events
func ComputeStatistics(events []parser.PingEvent) *Statistics {
	return ComputeStatisticsWithProgress(events, nil)
}

// ComputeStatisticsWithProgress computes statistics and reports progress
func ComputeStatisticsWithProgress(events []parser.PingEvent, progressCallback ProgressCallback) *Statistics {
	if len(events) == 0 {
		return &Statistics{}
	}
	
	// Overall statistics
	totalPackets := len(events)
	lostPackets := 0
	receivedPackets := 0
	rttAccumulator := NewWelfordAccumulator()
	
	// Consecutive loss tracking
	currentLossStreak := 0
	maxConsecutiveLoss := 0
	lossStreakHistogram := make(map[int]int)
	
	// Per-second aggregation
	buckets := make(map[int64]*perSecondBucket)
	
	progressInterval := 100000 // Report progress every 100k events
	
	// Process events
	for i, event := range events {
		// Report progress periodically
		if progressCallback != nil && i > 0 && i%progressInterval == 0 {
			progressCallback(i)
		}
		
		if event.IsLoss {
			lostPackets++
			currentLossStreak++
			if currentLossStreak > maxConsecutiveLoss {
				maxConsecutiveLoss = currentLossStreak
			}
		} else {
			// End of loss streak
			if currentLossStreak > 0 {
				lossStreakHistogram[currentLossStreak]++
				currentLossStreak = 0
			}
			receivedPackets++
			if !math.IsNaN(event.RTT) {
				rttAccumulator.Add(event.RTT)
			}
		}
		
		// Per-second aggregation
		sec := int64(math.Floor(event.Timestamp))
		bucket, exists := buckets[sec]
		if !exists {
			bucket = &perSecondBucket{
				rttAccumulator: NewWelfordAccumulator(),
			}
			buckets[sec] = bucket
		}
		
		if event.IsLoss {
			bucket.lossCount++
		} else if !math.IsNaN(event.RTT) {
			bucket.rttAccumulator.Add(event.RTT)
		}
		
		bucket.consecutiveLossEnd = currentLossStreak
		if currentLossStreak > bucket.maxStreakInSec {
			bucket.maxStreakInSec = currentLossStreak
		}
	}
	
	// Report final progress
	if progressCallback != nil && len(events) > 0 {
		progressCallback(len(events))
	}
	
	// Handle final loss streak
	if currentLossStreak > 0 {
		lossStreakHistogram[currentLossStreak]++
	}
	
	// Calculate loss rate
	lossRate := float64(lostPackets) / float64(totalPackets)
	
	// Build timeline
	timeline := buildTimeline(buckets)
	
	// Calculate max loss rate per second and total duration
	maxLossCount := 0
	var firstTimestamp, lastTimestamp int64
	firstTimestamp = math.MaxInt64
	lastTimestamp = math.MinInt64
	totalPacketsPerSecond := make(map[int64]int)
	
	for ts, bucket := range buckets {
		if bucket.lossCount > maxLossCount {
			maxLossCount = bucket.lossCount
		}
		if ts < firstTimestamp {
			firstTimestamp = ts
		}
		if ts > lastTimestamp {
			lastTimestamp = ts
		}
	}
	
	// Count total packets per second (to determine PPS)
	for _, event := range events {
		sec := int64(math.Floor(event.Timestamp))
		totalPacketsPerSecond[sec]++
	}
	
	// Find the maximum packets per second (expected rate)
	maxPacketsPerSecond := 0
	for _, count := range totalPacketsPerSecond {
		if count > maxPacketsPerSecond {
			maxPacketsPerSecond = count
		}
	}
	
	// Calculate max loss rate as a percentage
	var maxLossRatePerSec float64
	if maxPacketsPerSecond > 0 {
		maxLossRatePerSec = float64(maxLossCount) / float64(maxPacketsPerSecond)
	} else {
		maxLossRatePerSec = 0.0
	}
	
	// Calculate total duration
	var totalDurationSeconds float64
	if len(events) > 0 {
		if firstTimestamp != math.MaxInt64 && lastTimestamp != math.MinInt64 {
			totalDurationSeconds = float64(lastTimestamp - firstTimestamp + 1)
		} else {
			// Fallback: use event timestamps
			firstEventTime := events[0].Timestamp
			lastEventTime := events[len(events)-1].Timestamp
			totalDurationSeconds = lastEventTime - firstEventTime
		}
	}
	
	// Build histograms
	rttHist := buildRTTHistogram(events, defaultHistogramBins)
	lossHist := buildConsecutiveLossHistogram(lossStreakHistogram)
	
	return &Statistics{
		Summary: Summary{
			TotalPackets:         totalPackets,
			ReceivedPackets:      receivedPackets,
			LostPackets:          lostPackets,
			LossRate:             lossRate,
			MaxConsecutiveLoss:   maxConsecutiveLoss,
			MaxLossRatePerSec:    maxLossRatePerSec,
			MaxLostPerSec:        maxLossCount,
			TotalDurationSeconds: totalDurationSeconds,
			RTTMinMs:             rttAccumulator.Min(),
			RTTMeanMs:            rttAccumulator.Mean(),
			RTTStdMs:             rttAccumulator.StdDev(),
			RTTMaxMs:             rttAccumulator.Max(),
		},
		Timeline:   timeline,
		Histograms: Histograms{
			RTT:             rttHist,
			ConsecutiveLoss: lossHist,
		},
	}
}

// buildTimeline creates timeline data from per-second buckets
func buildTimeline(buckets map[int64]*perSecondBucket) Timeline {
	if len(buckets) == 0 {
		return Timeline{}
	}
	
	// Get sorted timestamps
	timestamps := make([]int64, 0, len(buckets))
	for ts := range buckets {
		timestamps = append(timestamps, ts)
	}
	sort.Slice(timestamps, func(i, j int) bool {
		return timestamps[i] < timestamps[j]
	})
	
	// Build arrays
	lossPerSec := make([]int, len(timestamps))
	consecutiveLossAtSecEnd := make([]int, len(timestamps))
	consecutiveLossMaxPerSec := make([]int, len(timestamps))
	rttMeanPerSec := make([]float64, len(timestamps))
	rttMinPerSec := make([]float64, len(timestamps))
	rttMaxPerSec := make([]float64, len(timestamps))
	
	for i, ts := range timestamps {
		bucket := buckets[ts]
		lossPerSec[i] = bucket.lossCount
		consecutiveLossAtSecEnd[i] = bucket.consecutiveLossEnd
		consecutiveLossMaxPerSec[i] = bucket.maxStreakInSec
		
		if bucket.rttAccumulator.Count() > 0 {
			rttMeanPerSec[i] = bucket.rttAccumulator.Mean()
			rttMinPerSec[i] = bucket.rttAccumulator.Min()
			rttMaxPerSec[i] = bucket.rttAccumulator.Max()
		} else {
			rttMeanPerSec[i] = math.NaN()
			rttMinPerSec[i] = math.NaN()
			rttMaxPerSec[i] = math.NaN()
		}
	}
	
	return Timeline{
		TsSeconds:                timestamps,
		LossPerSec:               lossPerSec,
		ConsecutiveLossAtSecEnd:  consecutiveLossAtSecEnd,
		ConsecutiveLossMaxPerSec: consecutiveLossMaxPerSec,
		RTTMeanPerSec:            rttMeanPerSec,
		RTTMinPerSec:             rttMinPerSec,
		RTTMaxPerSec:             rttMaxPerSec,
	}
}

// buildRTTHistogram creates RTT distribution histogram
func buildRTTHistogram(events []parser.PingEvent, numBins int) RTTHistogram {
	// First pass: collect RTT values and find min/max in one go
	rtts := make([]float64, 0, len(events)) // Pre-allocate with capacity
	var minRTT, maxRTT float64
	firstRTTFound := false
	
	for _, event := range events {
		if !event.IsLoss && !math.IsNaN(event.RTT) {
			rtts = append(rtts, event.RTT)
			if !firstRTTFound {
				minRTT = event.RTT
				maxRTT = event.RTT
				firstRTTFound = true
			} else {
				if event.RTT < minRTT {
					minRTT = event.RTT
				}
				if event.RTT > maxRTT {
					maxRTT = event.RTT
				}
			}
		}
	}
	
	if len(rtts) == 0 {
		return RTTHistogram{}
	}
	
	// Create bins
	binWidth := (maxRTT - minRTT) / float64(numBins)
	if binWidth <= 0 {
		binWidth = 1.0
	}
	
	binEdges := make([]float64, numBins+1)
	for i := 0; i <= numBins; i++ {
		binEdges[i] = minRTT + float64(i)*binWidth
	}
	
	// Count values in bins
	counts := make([]int, numBins)
	for _, rtt := range rtts {
		binIndex := int(math.Floor((rtt - minRTT) / binWidth))
		if binIndex >= numBins {
			binIndex = numBins - 1 // Overflow to last bin
		}
		if binIndex < 0 {
			binIndex = 0
		}
		counts[binIndex]++
	}
	
	return RTTHistogram{
		BinEdgesMs: binEdges,
		Counts:     counts,
	}
}

// buildConsecutiveLossHistogram creates consecutive loss run length histogram
func buildConsecutiveLossHistogram(streakHistogram map[int]int) ConsecutiveLossHistogram {
	if len(streakHistogram) == 0 {
		return ConsecutiveLossHistogram{}
	}
	
	// Get sorted run lengths
	runLengths := make([]int, 0, len(streakHistogram))
	for length := range streakHistogram {
		runLengths = append(runLengths, length)
	}
	sort.Ints(runLengths)
	
	// Build counts array
	counts := make([]int, len(runLengths))
	for i, length := range runLengths {
		counts[i] = streakHistogram[length]
	}
	
	return ConsecutiveLossHistogram{
		RunLengths: runLengths,
		Counts:     counts,
	}
}