package parser

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	// maxScanCapacity is the maximum buffer size for scanning large lines
	maxScanCapacity = 50 * 1024 * 1024 // 50MB
	// initialEventsCapacity is the initial capacity for events slice
	initialEventsCapacity = 1000
	// seqWrapAt is the icmp_seq modulus: the field is 16-bit (0–65535)
	seqWrapAt = 65536
)

// PingEvent represents a parsed ping event
type PingEvent struct {
	Timestamp  float64 // Unix timestamp with fractional seconds
	SeqNumber  int     // ICMP sequence number
	RTT        float64 // Round-trip time in milliseconds (NaN if lost)
	IsLoss     bool    // True if this is a lost packet
	IsExplicit bool    // True if loss was explicitly stated, false if inferred from gaps
}

// ParseResult contains the results of parsing a ping log
type ParseResult struct {
	Events      []PingEvent
	ParseErrors int
	FirstSeq    int // First seen sequence number
	LastSeq     int // Last seen sequence number
}

// ProgressCallback is called periodically during parsing to report progress
type ProgressCallback func(linesProcessed int, bytesRead int64)

// Parser handles parsing ping logs
type Parser struct {
	timestampRe              *regexp.Regexp
	replyRe                  *regexp.Regexp
	timeoutRe                *regexp.Regexp
	unreachableRe            *regexp.Regexp
	genericTimeoutRe         *regexp.Regexp
	summaryRe                *regexp.Regexp
	nonTimestampReplyRe      *regexp.Regexp
	nonTimestampTimeoutRe1   *regexp.Regexp
	nonTimestampTimeoutRe2   *regexp.Regexp
	
	pps              float64 // Packets per second for timestamp interpolation
	progressCallback ProgressCallback
}

// NewParser creates a new ping log parser
func NewParser(pps float64) *Parser {
	return &Parser{
		timestampRe:            regexp.MustCompile(`^\[([0-9]+\.[0-9]+)\]\s+`),
		replyRe:                regexp.MustCompile(`bytes from .*icmp_seq=([0-9]+).*time=([0-9]+(?:\.[0-9]+)?)\s*ms`),
		timeoutRe:              regexp.MustCompile(`no answer yet for icmp_seq=([0-9]+)`),
		unreachableRe:          regexp.MustCompile(`(?i)icmp_seq=([0-9]+).*Destination.*Unreachable`),
		genericTimeoutRe:       regexp.MustCompile(`(?i)request timed out`),
		summaryRe:              regexp.MustCompile(`^---.*ping statistics ---`),
		nonTimestampReplyRe:    regexp.MustCompile(`(\d+) bytes from ([^:]+): icmp_seq=(\d+) ttl=\d+ time=([0-9]+(?:\.[0-9]+)?)\s*ms`),
		nonTimestampTimeoutRe1: regexp.MustCompile(`Request timeout for icmp_seq (\d+)`),
		nonTimestampTimeoutRe2: regexp.MustCompile(`no answer yet for icmp_seq=(\d+)`),
		pps:                    pps,
	}
}

// SetProgressCallback sets a callback function to report parsing progress
func (p *Parser) SetProgressCallback(callback ProgressCallback) {
	p.progressCallback = callback
}

// Parse parses a ping log from the given reader
func (p *Parser) Parse(reader io.Reader) (*ParseResult, error) {
	scanner := bufio.NewScanner(reader)
	// Increase buffer size to handle very large lines
	buf := make([]byte, 0, 64*1024)       // 64KB initial
	scanner.Buffer(buf, maxScanCapacity)
	
	result := &ParseResult{
		Events:   make([]PingEvent, 0, initialEventsCapacity), // Pre-allocate with reasonable initial capacity
		FirstSeq: -1,
		LastSeq:  -1,
	}
	
	seqToTimestamp := make(map[int]float64)
	lastSeqSeen := -1
	syntheticTime := 0.0 // For non-timestamped logs
	linesProcessed := 0
	bytesRead := int64(0)
	progressInterval := 50000 // Report progress every 50k lines

	// Track whether we are currently skipping ping summary lines.
	inSummaryBlock := false

	for scanner.Scan() {
		linesProcessed++
		line := scanner.Text()
		bytesRead += int64(len(line)) + 1 // +1 for newline
		
		// Report progress periodically
		if p.progressCallback != nil && linesProcessed%progressInterval == 0 {
			p.progressCallback(linesProcessed, bytesRead)
		}
		line = strings.TrimSpace(line)
		
		// Skip empty lines
		if line == "" {
			continue
		}
		
		// Detect start of ping statistics summary block
		if p.summaryRe.MatchString(line) {
			inSummaryBlock = true
			lastSeqSeen = -1
			continue
		}

		// Process lines inside the summary block
		if inSummaryBlock {
			if strings.HasPrefix(line, "PING ") || p.timestampRe.MatchString(line) || p.nonTimestampReplyRe.MatchString(line) || p.nonTimestampTimeoutRe1.MatchString(line) || p.nonTimestampTimeoutRe2.MatchString(line) {
				inSummaryBlock = false
			} else {
				continue // skip all lines inside the summary block
			}
		}
		
		// Try to parse timestamp first
		timestampMatch := p.timestampRe.FindStringSubmatch(line)
		var event *PingEvent
		var timestamp float64
		
		if len(timestampMatch) >= 2 {
			// Timestamped line
			var err error
			timestamp, err = strconv.ParseFloat(timestampMatch[1], 64)
			if err != nil {
				result.ParseErrors++
				continue
			}
			event = p.parseEvent(line, timestamp)
		} else {
			// Non-timestamped line - try to parse and generate synthetic timestamp
			event = p.parseNonTimestampedEvent(line, syntheticTime)
			if event != nil {
				timestamp = event.Timestamp
				// For non-timestamped logs, increment synthetic time based on sequence
				// Use PPS to calculate time between packets
				if result.FirstSeq == -1 {
					syntheticTime = 0.0
				} else {
					syntheticTime += 1.0 / p.pps
				}
			}
		}
		
		if event == nil {
			// Could not parse this line as a ping event, continue
			continue
		}
		
		// Track sequence numbers
		if result.FirstSeq == -1 {
			result.FirstSeq = event.SeqNumber
		}
		result.LastSeq = event.SeqNumber
		
		// Store timestamp for sequence number
		seqToTimestamp[event.SeqNumber] = timestamp
		
		// Check for sequence gaps and fill in missing packets.
		// Two cases need handling:
		//   1. Normal forward gap: curSeq > lastSeqSeen+1
		//   2. Wrap-around gap: icmp_seq wrapped 65535→0 with losses near the boundary
		if lastSeqSeen != -1 {
			curSeq := event.SeqNumber
			if curSeq > lastSeqSeen+1 {
				// Normal forward gap
				for seq := lastSeqSeen + 1; seq < curSeq; seq++ {
					missingEvent := PingEvent{
						Timestamp:  p.interpolateTimestamp(seq, lastSeqSeen, curSeq, seqToTimestamp),
						SeqNumber:  seq,
						RTT:        math.NaN(),
						IsLoss:     true,
						IsExplicit: false,
					}
					result.Events = append(result.Events, missingEvent)
					seqToTimestamp[seq] = missingEvent.Timestamp
				}
			} else if curSeq < lastSeqSeen && (lastSeqSeen-curSeq) > seqWrapAt/2 {
				// Wrap-around: icmp_seq rolled over from 65535 back to 0 (or near 0).
				// Missing seqs: lastSeqSeen+1 … 65535, then 0 … curSeq-1
				prevTime := seqToTimestamp[lastSeqSeen]
				totalMissing := (seqWrapAt - 1 - lastSeqSeen) + curSeq
				injected := 0
				for seq := lastSeqSeen + 1; seq <= seqWrapAt-1; seq++ {
					injected++
					ts := prevTime + float64(injected)/p.pps
					missingEvent := PingEvent{
						Timestamp:  ts,
						SeqNumber:  seq,
						RTT:        math.NaN(),
						IsLoss:     true,
						IsExplicit: false,
					}
					result.Events = append(result.Events, missingEvent)
					seqToTimestamp[seq] = ts
				}
				for seq := 0; seq < curSeq; seq++ {
					injected++
					ts := prevTime + float64(injected)/p.pps
					missingEvent := PingEvent{
						Timestamp:  ts,
						SeqNumber:  seq,
						RTT:        math.NaN(),
						IsLoss:     true,
						IsExplicit: false,
					}
					result.Events = append(result.Events, missingEvent)
					seqToTimestamp[seq] = ts
				}
				_ = totalMissing // used implicitly via injected
			}
		}
		
		result.Events = append(result.Events, *event)
		lastSeqSeen = event.SeqNumber
	}
	
	// Report final progress
	if p.progressCallback != nil && linesProcessed > 0 {
		p.progressCallback(linesProcessed, bytesRead)
	}
	
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning file: %w", err)
	}
	
	return result, nil
}

// parseEvent attempts to parse a single line into a ping event
func (p *Parser) parseEvent(line string, timestamp float64) *PingEvent {
	// Try to parse as a reply
	if replyMatch := p.replyRe.FindStringSubmatch(line); len(replyMatch) >= 3 {
		seq, err := strconv.Atoi(replyMatch[1])
		if err != nil {
			return nil
		}
		
		rtt, err := strconv.ParseFloat(replyMatch[2], 64)
		if err != nil {
			return nil
		}
		
		return &PingEvent{
			Timestamp:  timestamp,
			SeqNumber:  seq,
			RTT:        rtt,
			IsLoss:     false,
			IsExplicit: false,
		}
	}
	
	// Try to parse as "no answer yet" timeout
	if timeoutMatch := p.timeoutRe.FindStringSubmatch(line); len(timeoutMatch) >= 2 {
		seq, err := strconv.Atoi(timeoutMatch[1])
		if err != nil {
			return nil
		}
		
		return &PingEvent{
			Timestamp:  timestamp,
			SeqNumber:  seq,
			RTT:        math.NaN(),
			IsLoss:     true,
			IsExplicit: true,
		}
	}
	
	// Try to parse as destination unreachable
	if unreachableMatch := p.unreachableRe.FindStringSubmatch(line); len(unreachableMatch) >= 2 {
		seq, err := strconv.Atoi(unreachableMatch[1])
		if err != nil {
			return nil
		}
		
		return &PingEvent{
			Timestamp:  timestamp,
			SeqNumber:  seq,
			RTT:        math.NaN(),
			IsLoss:     true,
			IsExplicit: true,
		}
	}
	
	// Check for generic timeout (no sequence number)
	if p.genericTimeoutRe.MatchString(line) {
		// We can't extract sequence number, so we'll rely on gap detection
		// Return nil to skip this line for now
		return nil
	}
	
	return nil
}

// parseNonTimestampedEvent attempts to parse a line without timestamps
func (p *Parser) parseNonTimestampedEvent(line string, syntheticTimestamp float64) *PingEvent {
	// Try to parse non-timestamped ping replies
	// Pattern: 64 bytes from 192.168.1.1: icmp_seq=1 ttl=64 time=12.345 ms
	// Also handles larger packet sizes like: 65008 bytes from 192.168.0.1: icmp_seq=1 ttl=64 time=12.4 ms
	
	if replyMatch := p.nonTimestampReplyRe.FindStringSubmatch(line); len(replyMatch) >= 5 {
		seq, err := strconv.Atoi(replyMatch[3])
		if err != nil {
			return nil
		}
		
		rtt, err := strconv.ParseFloat(replyMatch[4], 64)
		if err != nil {
			return nil
		}
		
		// Calculate synthetic timestamp based on sequence number and PPS
		timestamp := float64(seq-1) / p.pps
		
		return &PingEvent{
			Timestamp:  timestamp,
			SeqNumber:  seq,
			RTT:        rtt,
			IsLoss:     false,
			IsExplicit: false,
		}
	}
	
	// Try to parse timeout patterns for non-timestamped logs
	timeoutMatch := p.nonTimestampTimeoutRe1.FindStringSubmatch(line)
	if timeoutMatch == nil {
		timeoutMatch = p.nonTimestampTimeoutRe2.FindStringSubmatch(line)
	}
	
	if len(timeoutMatch) >= 2 {
		seq, err := strconv.Atoi(timeoutMatch[1])
		if err != nil {
			return nil
		}
		
		// Calculate synthetic timestamp based on sequence number and PPS
		timestamp := float64(seq-1) / p.pps
		
		return &PingEvent{
			Timestamp:  timestamp,
			SeqNumber:  seq,
			RTT:        math.NaN(),
			IsLoss:     true,
			IsExplicit: true,
		}
	}
	
	return nil
}

// interpolateTimestamp calculates the timestamp for a missing sequence number
func (p *Parser) interpolateTimestamp(missingSeq, prevSeq, nextSeq int, seqToTimestamp map[int]float64) float64 {
	prevTime, hasPrev := seqToTimestamp[prevSeq]
	nextTime, hasNext := seqToTimestamp[nextSeq]
	
	if hasPrev && hasNext {
		// Interpolate between known timestamps
		ratio := float64(missingSeq-prevSeq) / float64(nextSeq-prevSeq)
		return prevTime + ratio*(nextTime-prevTime)
	} else if hasPrev {
		// Extrapolate forward using PPS
		seqDiff := missingSeq - prevSeq
		return prevTime + float64(seqDiff)/p.pps
	} else if hasNext {
		// Extrapolate backward using PPS
		seqDiff := nextSeq - missingSeq
		return nextTime - float64(seqDiff)/p.pps
	} else {
		// No reference points, use PPS from sequence 0
		return float64(missingSeq) / p.pps
	}
}