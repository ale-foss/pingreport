package app

import (
	"bytes"
	"errors"
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
)

// debugState captures diagnostic output for every run.
// In verbose mode (--debug or pingreport-debug.exe) all log lines also go to
// stderr in real time. In non-verbose mode they accumulate silently in the
// buffer so they can be written to a log file if the run fails.
type debugState struct {
	verbose bool
	logger  *log.Logger
	buf     bytes.Buffer
	// resolvedOutputPath is set as soon as the HTML output path is known.
	// writeLogFile uses it as a candidate save location.
	resolvedOutputPath string
}

func newDebugState(verbose bool) *debugState {
	d := &debugState{verbose: verbose}
	var w io.Writer
	if verbose {
		w = io.MultiWriter(os.Stderr, &d.buf)
	} else {
		w = &d.buf
	}
	d.logger = log.New(w, "[DBG] ", log.Ltime|log.Lmicroseconds)
	return d
}

// log appends a formatted line to the buffer (and to stderr when verbose).
func (d *debugState) log(format string, args ...interface{}) {
	d.logger.Printf(format, args...)
}

// setOutputPath records the resolved HTML output path for use by writeLogFile.
func (d *debugState) setOutputPath(path string) {
	d.resolvedOutputPath = path
}

// logSyscallError unwraps PathError / LinkError and logs the Windows error code.
// No-op when not verbose.
func (d *debugState) logSyscallError(err error) {
	if !d.verbose || err == nil {
		return
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		d.log("  +- PathError: op=%q path=%q", pathErr.Op, pathErr.Path)
		if errno, ok := pathErr.Err.(syscall.Errno); ok {
			d.log("  +- Windows error code: %d (0x%08X) � %s", uint32(errno), uint32(errno), errno.Error())
		}
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		d.log("  +- LinkError: op=%q old=%q new=%q", linkErr.Op, linkErr.Old, linkErr.New)
		if errno, ok := linkErr.Err.(syscall.Errno); ok {
			d.log("  +- Windows error code: %d (0x%08X) � %s", uint32(errno), uint32(errno), errno.Error())
		}
	}
}

// logSystemInfo logs OS / user / directory details. Verbose-only.
func (d *debugState) logSystemInfo() {
	if !d.verbose {
		return
	}
	d.log("=== SYSTEM INFO ===")
	d.log("pingreport v%s", Version)
	d.log("OS/Arch: %s/%s | Go: %s", runtime.GOOS, runtime.GOARCH, runtime.Version())

	wd, _ := os.Getwd()
	d.log("Working directory: %q", wd)

	if u, err := user.Current(); err == nil {
		d.log("Current user: %s (uid=%s, gid=%s, home=%s)", u.Username, u.Uid, u.Gid, u.HomeDir)
	} else {
		d.log("Could not retrieve current user: %v", err)
	}

	tmpDir := os.TempDir()
	d.log("Temp directory: %q", tmpDir)
	d.probeWrite(tmpDir, "temp-dir write probe")

	dl := downloadsDir()
	d.log("Downloads directory: %q", dl)
	d.probeWrite(dl, "downloads write probe")

	d.log("===================")
}

// probeWrite creates and immediately removes a temp file to verify write access.
// Verbose-only.
func (d *debugState) probeWrite(dir, label string) {
	if !d.verbose {
		return
	}
	tmp, err := os.CreateTemp(dir, "pingreport-probe-*.tmp")
	if err != nil {
		d.log("  WRITE PROBE FAILED [%s] in %q: %v", label, dir, err)
		d.logSyscallError(err)
		return
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name)
	d.log("  write probe OK [%s]: created and removed %q", label, name)
}

// checkDirAccess logs Stat + ReadDir results. Verbose-only.
func (d *debugState) checkDirAccess(dir string) {
	if !d.verbose {
		return
	}
	d.log("--- Directory access check: %q ---", dir)
	info, err := os.Stat(dir)
	if err != nil {
		d.log("  os.Stat FAILED: %v", err)
		d.logSyscallError(err)
		return
	}
	d.log("  os.Stat OK: mode=%s isDir=%v modtime=%s",
		info.Mode(), info.IsDir(), info.ModTime().Format(time.RFC3339))

	entries, err := os.ReadDir(dir)
	if err != nil {
		d.log("  os.ReadDir FAILED: %v", err)
		d.logSyscallError(err)
		return
	}
	d.log("  os.ReadDir OK: %d entries", len(entries))
	for _, e := range entries {
		ei, statErr := e.Info()
		if statErr != nil {
			d.log("    %-40s  (DirEntry.Info error: %v)", e.Name(), statErr)
			continue
		}
		d.log("    %-40s  mode=%-12s  size=%d", e.Name(), ei.Mode(), ei.Size())
	}
}

// checkFileReadAccess probes stat, open, and a 128-byte read. Verbose-only.
func (d *debugState) checkFileReadAccess(path string) {
	if !d.verbose {
		return
	}
	d.log("--- File read access check: %q ---", path)
	info, err := os.Stat(path)
	if err != nil {
		d.log("  os.Stat FAILED: %v", err)
		d.logSyscallError(err)
		return
	}
	d.log("  os.Stat OK: mode=%s size=%d modtime=%s",
		info.Mode(), info.Size(), info.ModTime().Format(time.RFC3339))

	f, err := os.Open(path)
	if err != nil {
		d.log("  os.Open FAILED: %v", err)
		d.logSyscallError(err)
		return
	}
	defer f.Close()
	d.log("  os.Open OK")

	buf := make([]byte, 128)
	n, readErr := f.Read(buf)
	if readErr != nil && readErr != io.EOF {
		d.log("  f.Read FAILED after %d bytes: %v", n, readErr)
		d.logSyscallError(readErr)
	} else {
		d.log("  f.Read OK: %d bytes (first line preview: %q)", n,
			strings.SplitN(string(buf[:n]), "\n", 2)[0])
	}
}

// checkOutputWriteAccess probes MkdirAll + os.Create on path (then removes the
// created file). Verbose-only.
func (d *debugState) checkOutputWriteAccess(path string) {
	if !d.verbose {
		return
	}
	d.log("--- Output write access check: %q ---", path)
	dir := filepath.Dir(path)
	info, statErr := os.Stat(dir)
	if statErr != nil {
		d.log("  Output directory %q does not yet exist (will attempt MkdirAll)", dir)
	} else {
		d.log("  Output directory stat: mode=%s isDir=%v", info.Mode(), info.IsDir())
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		d.log("  os.MkdirAll(%q) FAILED: %v", dir, err)
		d.logSyscallError(err)
		return
	}
	d.log("  os.MkdirAll(%q) OK", dir)
	f, err := os.Create(path)
	if err != nil {
		d.log("  os.Create(%q) FAILED: %v", path, err)
		d.logSyscallError(err)
		return
	}
	f.Close()
	os.Remove(path)
	d.log("  os.Create write probe OK (test file removed)")
	d.probeWrite(dir, "output directory write probe")
}

// canWriteDirLogged tests writability of dir via a temp-file probe.
// Logs results when verbose.
func (d *debugState) canWriteDirLogged(dir, label string) bool {
	if err := os.MkdirAll(dir, 0755); err != nil {
		if d.verbose {
			d.log("  canWriteToDir [%s] MkdirAll(%q) FAILED: %v", label, dir, err)
			d.logSyscallError(err)
		}
		return false
	}
	tmp, err := os.CreateTemp(dir, "pingreport-probe-*.tmp")
	if err != nil {
		if d.verbose {
			d.log("  canWriteToDir [%s] CreateTemp in %q FAILED: %v", label, dir, err)
			d.logSyscallError(err)
		}
		return false
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name)
	if d.verbose {
		d.log("  canWriteToDir [%s] OK � created and removed %q", label, name)
	}
	return true
}

// writeLogFile saves the accumulated log buffer to the first writable location.
// Priority: Downloads > report directory > cwd > temp dir.
// Returns the full path of the saved file, or "" if all candidates failed.
func (d *debugState) writeLogFile() string {
	logFileName := fmt.Sprintf("pingreport-debug-%s.log", time.Now().Format("20060102-150405"))

	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "Downloads"))
	}
	if d.resolvedOutputPath != "" {
		candidates = append(candidates, filepath.Dir(d.resolvedOutputPath))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	candidates = append(candidates, os.TempDir())

	for _, dir := range candidates {
		fullPath := filepath.Join(dir, logFileName)
		if err := os.WriteFile(fullPath, d.buf.Bytes(), 0644); err == nil {
			return fullPath
		}
	}
	return ""
}