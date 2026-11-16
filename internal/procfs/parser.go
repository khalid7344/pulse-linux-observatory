package procfs

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	errInvalidStatHeader = errors.New("invalid /proc/stat cpu header")
	errMalformedStatFile = errors.New("malformed /proc/[pid]/stat file")
)

// SanitizeString strips or replaces ASCII control characters (< 0x20 and 0x7f)
// with spaces to prevent terminal escape injection.
func SanitizeString(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b == 0x7f {
			buf.WriteByte(' ')
		} else {
			buf.WriteByte(b)
		}
	}
	return strings.TrimSpace(buf.String())
}

// ParseSystemStat parses /proc/stat bytes to extract aggregate CPU jiffies and core count.
func ParseSystemStat(data []byte) (totalJiffies, activeJiffies uint64, numCores int, err error) {
	lines := bytes.Split(data, []byte("\n"))
	cpuFound := false

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		if bytes.HasPrefix(line, []byte("cpu ")) {
			// Aggregate CPU line
			fields := bytes.Fields(line[4:])
			if len(fields) < 4 {
				return 0, 0, 0, errInvalidStatHeader
			}

			// guest and guest_nice are already included in user and nice.
			var counters [8]uint64
			for i := 0; i < len(fields) && i < len(counters); i++ {
				v, parseErr := strconv.ParseUint(string(fields[i]), 10, 64)
				if parseErr != nil || ^uint64(0)-totalJiffies < v {
					return 0, 0, 0, errInvalidStatHeader
				}
				counters[i] = v
				totalJiffies += v
			}
			activeJiffies = totalJiffies - counters[3] - counters[4]
			cpuFound = true
		} else if bytes.HasPrefix(line, []byte("cpu")) {
			// Per-core line: cpu0, cpu1, etc.
			// Verify that what follows 'cpu' is a digit
			rest := line[3:]
			if len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
				numCores++
			}
		}
	}

	if !cpuFound {
		return 0, 0, 0, errInvalidStatHeader
	}
	if numCores == 0 {
		numCores = 1
	}

	return totalJiffies, activeJiffies, numCores, nil
}

// ParseMeminfo parses /proc/meminfo bytes into system memory and swap counters in bytes.
func ParseMeminfo(data []byte) (memTotal, memAvail, memUsed, swapTotal, swapUsed uint64, err error) {
	lines := bytes.Split(data, []byte("\n"))
	var memFree, buffers, cached, swapFree uint64
	var hasMemAvail bool

	for _, line := range lines {
		colonIdx := bytes.IndexByte(line, ':')
		if colonIdx <= 0 {
			continue
		}
		key := string(bytes.TrimSpace(line[:colonIdx]))
		switch key {
		case "MemTotal", "MemAvailable", "MemFree", "Buffers", "Cached", "SwapTotal", "SwapFree":
		default:
			continue
		}
		valBytes := bytes.TrimSpace(line[colonIdx+1:])

		// Values are formatted as "<number> kB"
		spaceIdx := bytes.IndexByte(valBytes, ' ')
		var numStr string
		if spaceIdx > 0 {
			numStr = string(valBytes[:spaceIdx])
		} else {
			numStr = string(valBytes)
		}

		valKB, parseErr := strconv.ParseUint(numStr, 10, 64)
		if parseErr != nil || valKB > ^uint64(0)/1024 {
			return 0, 0, 0, 0, 0, fmt.Errorf("invalid meminfo field %s", key)
		}
		valBytesTotal := valKB * 1024

		switch key {
		case "MemTotal":
			memTotal = valBytesTotal
		case "MemFree":
			memFree = valBytesTotal
		case "MemAvailable":
			memAvail = valBytesTotal
			hasMemAvail = true
		case "Buffers":
			buffers = valBytesTotal
		case "Cached":
			cached = valBytesTotal
		case "SwapTotal":
			swapTotal = valBytesTotal
		case "SwapFree":
			swapFree = valBytesTotal
		}
	}

	if memTotal == 0 {
		return 0, 0, 0, 0, 0, errors.New("missing or invalid MemTotal")
	}
	if hasMemAvail {
		if memTotal >= memAvail {
			memUsed = memTotal - memAvail
		}
	} else {
		// Fallback: MemTotal - (MemFree + Buffers + Cached)
		nonUsed := memFree + buffers + cached
		if nonUsed > memTotal {
			nonUsed = memTotal
		}
		memAvail = nonUsed
		if memTotal >= nonUsed {
			memUsed = memTotal - nonUsed
		}
	}

	if swapTotal >= swapFree {
		swapUsed = swapTotal - swapFree
	}

	return memTotal, memAvail, memUsed, swapTotal, swapUsed, nil
}

// ParsedProcStat holds intermediate values parsed from /proc/[pid]/stat.
type ParsedProcStat struct {
	PID          int
	Comm         string
	State        byte
	PPID         int
	UTimeJiffies uint64
	STimeJiffies uint64
	NumThreads   int
	StartTime    uint64
	VMSBytes     uint64
	RSSBytes     uint64
}

// ParsePIDStat parses a single /proc/[pid]/stat line.
// It uses first '(' and last ')' to handle comm strings with parens, spaces, and special characters.
func ParsePIDStat(data []byte, pageSize uint64) (ParsedProcStat, error) {
	openParen := bytes.IndexByte(data, '(')
	closeParen := bytes.LastIndexByte(data, ')')

	if openParen < 0 || closeParen < 0 || closeParen <= openParen {
		return ParsedProcStat{}, errMalformedStatFile
	}

	pidStr := string(bytes.TrimSpace(data[:openParen]))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return ParsedProcStat{}, fmt.Errorf("%w: invalid pid: %v", errMalformedStatFile, err)
	}

	rawComm := string(data[openParen+1 : closeParen])
	comm := SanitizeString(rawComm)

	// Fields after the closing ')'
	rest := bytes.TrimSpace(data[closeParen+1:])
	fields := bytes.Fields(rest)

	// In /proc/[pid]/stat:
	// Field 1: pid (before paren)
	// Field 2: comm (inside parens)
	// Field 3: state -> fields[0]
	// Field 4: ppid -> fields[1]
	// ...
	// Field 14: utime -> fields[11]
	// Field 15: stime -> fields[12]
	// ...
	// Field 20: num_threads -> fields[17]
	// Field 21: itrealvalue -> fields[18]
	// Field 22: starttime -> fields[19]
	// Field 23: vsize -> fields[20]
	// Field 24: rss (pages) -> fields[21]
	if len(fields) < 22 {
		return ParsedProcStat{}, fmt.Errorf("%w: expected at least 22 fields after comm, got %d", errMalformedStatFile, len(fields))
	}

	var state byte = '?'
	if len(fields[0]) > 0 {
		state = fields[0][0]
	}

	ppid, err := strconv.Atoi(string(fields[1]))
	if err != nil || ppid < 0 || pid <= 0 || len(fields[0]) != 1 || !strings.ContainsRune("RSDZTtXxKWPIN", rune(state)) {
		return ParsedProcStat{}, errMalformedStatFile
	}
	var values [6]uint64
	for i, index := range []int{11, 12, 17, 19, 20, 21} {
		v, err := strconv.ParseUint(string(fields[index]), 10, 64)
		if err != nil {
			return ParsedProcStat{}, fmt.Errorf("%w: field %d: %v", errMalformedStatFile, index+3, err)
		}
		values[i] = v
	}
	utime, stime, startTime, vsize, rssPages := values[0], values[1], values[3], values[4], values[5]
	numThreads := int(values[2])
	if numThreads < 0 || ^uint64(0)-utime < stime {
		return ParsedProcStat{}, errMalformedStatFile
	}

	if pageSize == 0 {
		pageSize = 4096
	}
	if rssPages > ^uint64(0)/pageSize {
		return ParsedProcStat{}, errMalformedStatFile
	}
	rssBytes := rssPages * pageSize

	return ParsedProcStat{
		PID:          pid,
		Comm:         comm,
		State:        state,
		PPID:         ppid,
		UTimeJiffies: utime,
		STimeJiffies: stime,
		NumThreads:   numThreads,
		StartTime:    startTime,
		VMSBytes:     vsize,
		RSSBytes:     rssBytes,
	}, nil
}

// ParsePIDStatus parses /proc/[pid]/status to extract UID and optionally VmRSS/VmSize.
func ParsePIDStatus(data []byte) (uid uint32, vmRSSBytes uint64, vmSizeBytes uint64) {
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		colonIdx := bytes.IndexByte(line, ':')
		if colonIdx <= 0 {
			continue
		}
		key := string(bytes.TrimSpace(line[:colonIdx]))
		val := bytes.TrimSpace(line[colonIdx+1:])

		switch key {
		case "Uid":
			fields := bytes.Fields(val)
			if len(fields) > 0 {
				parsedUID, err := strconv.ParseUint(string(fields[0]), 10, 32)
				if err == nil {
					uid = uint32(parsedUID)
				}
			}
		case "VmRSS":
			fields := bytes.Fields(val)
			if len(fields) > 0 {
				kb, err := strconv.ParseUint(string(fields[0]), 10, 64)
				if err == nil {
					vmRSSBytes = kb * 1024
				}
			}
		case "VmSize":
			fields := bytes.Fields(val)
			if len(fields) > 0 {
				kb, err := strconv.ParseUint(string(fields[0]), 10, 64)
				if err == nil {
					vmSizeBytes = kb * 1024
				}
			}
		}
	}
	return uid, vmRSSBytes, vmSizeBytes
}

// ParsePIDCmdline parses /proc/[pid]/cmdline bytes, converting NUL delimiters to spaces
// and sanitizing control characters. If empty, falls back to "[comm]".
func ParsePIDCmdline(data []byte, comm string) string {
	if len(data) == 0 {
		if comm != "" {
			return "[" + comm + "]"
		}
		return ""
	}

	// Split by NUL bytes
	args := bytes.Split(data, []byte{0})
	var parts []string
	for _, arg := range args {
		if len(arg) > 0 {
			parts = append(parts, string(arg))
		}
	}

	if len(parts) == 0 {
		if comm != "" {
			return "[" + comm + "]"
		}
		return ""
	}

	joined := strings.Join(parts, " ")
	sanitized := SanitizeString(joined)
	if sanitized == "" && comm != "" {
		return "[" + comm + "]"
	}
	return sanitized
}

// ParseSmapsRollup parses /proc/[pid]/smaps_rollup to extract Pss in bytes.
func ParseSmapsRollup(data []byte) (pssBytes uint64, found bool) {
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("Pss:")) {
			fields := bytes.Fields(line[4:])
			if len(fields) > 0 {
				kb, err := strconv.ParseUint(string(fields[0]), 10, 64)
				if err == nil {
					return kb * 1024, true
				}
			}
		}
	}
	return 0, false
}
