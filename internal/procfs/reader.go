package procfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/khalid7344/pulse-linux-observatory/internal/model"
)

// FSCollector collects raw system and process samples from an io/fs.FS filesystem.
type FSCollector struct {
	fsys      fs.FS
	pageSize  uint64
	enablePSS bool
}

// NewFSCollector creates a new FSCollector using the provided fs.FS.
func NewFSCollector(fsys fs.FS, pageSize uint64, enablePSS bool) *FSCollector {
	if pageSize == 0 {
		pageSize = 4096
	}
	return &FSCollector{
		fsys:      fsys,
		pageSize:  pageSize,
		enablePSS: enablePSS,
	}
}

// NewHostCollector creates a collector pointed at the host proc root (default "/proc").
func NewHostCollector(procRoot string, pageSize uint64, enablePSS bool) *FSCollector {
	if procRoot == "" {
		procRoot = "/proc"
	}
	if pageSize == 0 {
		ps := os.Getpagesize()
		if ps > 0 {
			pageSize = uint64(ps)
		} else {
			pageSize = 4096
		}
	}
	return NewFSCollector(os.DirFS(procRoot), pageSize, enablePSS)
}

// isNumeric returns true if s consists solely of ASCII decimal digits.
func isNumeric(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// readFileBuffered reads a file from fsys with an initial pre-allocated buffer
// to minimize slice reallocation overhead on Linux pseudo-files (which report 0 size).
func readFileBuffered(fsys fs.FS, name string, initCap int) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	const maxFileBytes = 4 << 20
	buf := make([]byte, 0, initCap)
	tmp := make([]byte, 1024)
	for {
		n, rErr := f.Read(tmp)
		if n > 0 {
			if len(buf)+n > maxFileBytes {
				return nil, fmt.Errorf("proc file %s exceeds %d bytes", name, maxFileBytes)
			}
			buf = append(buf, tmp[:n]...)
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			return buf, rErr
		}
	}
	return buf, nil
}

// isNotExist checks if an error indicates a missing file or vanished process.
func isNotExist(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err)
}

// isPermission checks if an error indicates permission denied.
func isPermission(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, fs.ErrPermission) || os.IsPermission(err)
}

// Collect reads /proc files to generate a RawSystemSample.
func (c *FSCollector) Collect(ctx context.Context) (model.RawSystemSample, error) {
	sample := model.RawSystemSample{
		Timestamp: time.Now(),
	}

	// 1. Read /proc/stat
	statBytes, err := readFileBuffered(c.fsys, "stat", 2048)
	if err != nil {
		return sample, fmt.Errorf("read /proc/stat: %w", err)
	}

	totalJiffies, activeJiffies, numCores, err := ParseSystemStat(statBytes)
	if err != nil {
		return sample, fmt.Errorf("parse /proc/stat: %w", err)
	}
	sample.TotalJiffies = totalJiffies
	sample.ActiveJiffies = activeJiffies
	sample.NumCores = numCores

	// 2. Read /proc/meminfo
	meminfoBytes, err := readFileBuffered(c.fsys, "meminfo", 2048)
	if err != nil {
		return sample, fmt.Errorf("read /proc/meminfo: %w", err)
	}

	memTotal, memAvail, memUsed, swapTotal, swapUsed, err := ParseMeminfo(meminfoBytes)
	if err != nil {
		return sample, fmt.Errorf("parse /proc/meminfo: %w", err)
	}
	sample.MemTotalBytes = memTotal
	sample.MemAvailableBytes = memAvail
	sample.MemUsedBytes = memUsed
	sample.SwapTotalBytes = swapTotal
	sample.SwapUsedBytes = swapUsed

	// 3. Scan numeric PID directories
	entries, err := fs.ReadDir(c.fsys, ".")
	if err != nil {
		return sample, fmt.Errorf("readdir /proc: %w", err)
	}

	// Pre-allocate slice with reasonable estimation
	procs := make([]model.RawProcessSample, 0, len(entries))

	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return sample, ctx.Err()
		default:
		}

		entryName := entry.Name()
		if !entry.IsDir() && entry.Type()&fs.ModeDir == 0 {
			// Some virtual filesystems may not mark entries as Dir in ReadDir
			// So verify numeric name first
			if !isNumeric(entryName) {
				continue
			}
		} else if !isNumeric(entryName) {
			continue
		}

		pid, err := strconv.Atoi(entryName)
		if err != nil {
			continue
		}

		// Read /proc/[pid]/stat
		pidStatPath := filepath.Join(entryName, "stat")
		pStatBytes, err := readFileBuffered(c.fsys, pidStatPath, 1024)
		if err != nil {
			if isNotExist(err) {
				sample.SkippedVanished++
			} else if isPermission(err) {
				sample.SkippedPermission++
			} else {
				sample.ParseErrors++
			}
			continue
		}

		parsedStat, err := ParsePIDStat(pStatBytes, c.pageSize)
		if err != nil || parsedStat.PID != pid {
			sample.ParseErrors++
			continue
		}

		// Read /proc/[pid]/status (optional: UID, etc.)
		var uid uint32
		pidStatusPath := filepath.Join(entryName, "status")
		if statusBytes, err := readFileBuffered(c.fsys, pidStatusPath, 1024); err == nil {
			uid, _, _ = ParsePIDStatus(statusBytes)
		}

		// Read /proc/[pid]/cmdline (optional: fall back to [comm])
		var cmdline string
		pidCmdlinePath := filepath.Join(entryName, "cmdline")
		if cmdBytes, err := readFileBuffered(c.fsys, pidCmdlinePath, 512); err == nil {
			cmdline = ParsePIDCmdline(cmdBytes, parsedStat.Comm)
		} else {
			cmdline = "[" + parsedStat.Comm + "]"
		}

		// Read /proc/[pid]/smaps_rollup if enabled
		var pssBytes uint64
		var pssValid bool
		if c.enablePSS {
			pidSmapsPath := filepath.Join(entryName, "smaps_rollup")
			if smapsBytes, err := readFileBuffered(c.fsys, pidSmapsPath, 2048); err == nil {
				pssBytes, pssValid = ParseSmapsRollup(smapsBytes)
			}
			// Unreadable or permission denied smaps_rollup gracefully sets pssValid=false without error
		}

		procSample := model.RawProcessSample{
			Key: model.ProcessKey{
				PID:       pid,
				StartTime: parsedStat.StartTime,
			},
			PPID:         parsedStat.PPID,
			UID:          uid,
			Comm:         parsedStat.Comm,
			Cmdline:      cmdline,
			State:        parsedStat.State,
			UTimeJiffies: parsedStat.UTimeJiffies,
			STimeJiffies: parsedStat.STimeJiffies,
			NumThreads:   parsedStat.NumThreads,
			VMSBytes:     parsedStat.VMSBytes,
			RSSBytes:     parsedStat.RSSBytes,
			PSSBytes:     pssBytes,
			PSSValid:     pssValid,
		}

		procs = append(procs, procSample)
	}

	sample.Processes = procs
	return sample, nil
}
