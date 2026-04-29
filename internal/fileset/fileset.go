package fileset

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNoFiles is returned by Discover when the directory contains no PingResult_*.txt files.
var ErrNoFiles = errors.New("no PingResult_*.txt files found")

// Discover scans dir for files matching PingResult_*.txt and returns their
// absolute paths sorted in ascending order by the timestamp embedded in the
// filename (e.g. PingResult_202603031630.txt → "202603031630").
// Non-matching files in the directory are silently ignored.
func Discover(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read directory %q: %w", dir, err)
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if matchesPingResult(name) {
			paths = append(paths, filepath.Join(dir, name))
		}
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("%w in %q", ErrNoFiles, dir)
	}

	sort.Slice(paths, func(i, j int) bool {
		return timestampFromName(filepath.Base(paths[i])) < timestampFromName(filepath.Base(paths[j]))
	})

	return paths, nil
}

// matchesPingResult returns true for filenames like PingResult_<anything>.txt
func matchesPingResult(name string) bool {
	return strings.HasPrefix(name, "PingResult_") && strings.HasSuffix(name, ".txt")
}

// timestampFromName extracts the part between "PingResult_" and ".txt".
// Lexicographic comparison on the returned string gives chronological order
// when the timestamp format is YYYYMMDDHHmm (or similar zero-padded numeric).
func timestampFromName(name string) string {
	trimmed := strings.TrimPrefix(name, "PingResult_")
	trimmed = strings.TrimSuffix(trimmed, ".txt")
	return trimmed
}

// multiReadCloser chains multiple files and closes all of them on Close.
type multiReadCloser struct {
	files   []*os.File
	reader  io.Reader
}

func (m *multiReadCloser) Read(p []byte) (int, error) {
	return m.reader.Read(p)
}

func (m *multiReadCloser) Close() error {
	var firstErr error
	for _, f := range m.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// NewMultiReader opens the given file paths in order and returns a single
// io.ReadCloser that reads them sequentially as if they were one stream.
func NewMultiReader(paths []string) (io.ReadCloser, error) {
	files := make([]*os.File, 0, len(paths))
	readers := make([]io.Reader, 0, len(paths))

	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			// Close already-opened files before returning the error.
			for _, opened := range files {
				opened.Close()
			}
			return nil, fmt.Errorf("cannot open %q: %w", p, err)
		}
		files = append(files, f)
		readers = append(readers, f)
	}

	return &multiReadCloser{
		files:  files,
		reader: io.MultiReader(readers...),
	}, nil
}
