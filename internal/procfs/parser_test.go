package procfs

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/khalid7344/pulse-linux-observatory/internal/model"
)

func TestParseSystemStat(t *testing.T) {
	data := []byte(`cpu  10000 0 2000 88000 0 0 0 0 0 0
cpu0 2500 0 500 22000 0 0 0 0 0 0
cpu1 2500 0 500 22000 0 0 0 0 0 0
cpu2 2500 0 500 22000 0 0 0 0 0 0
cpu3 2500 0 500 22000 0 0 0 0 0 0
`)
	total, active, cores, err := ParseSystemStat(data)
	if err != nil {
		t.Fatalf("ParseSystemStat failed: %v", err)
	}
	if total != 100000 {
		t.Errorf("expected total 100000, got %d", total)
	}
	if active != 12000 {
		t.Errorf("expected active 12000, got %d", active)
	}
	if cores != 4 {
		t.Errorf("expected 4 cores, got %d", cores)
	}
}

func TestParseMeminfo(t *testing.T) {
	data := []byte(`MemTotal:       16384000 kB
MemFree:         4096000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          3584000 kB
SwapTotal:       4096000 kB
SwapFree:        4096000 kB
`)
	total, avail, used, swapTotal, swapUsed, err := ParseMeminfo(data)
	if err != nil {
		t.Fatalf("ParseMeminfo failed: %v", err)
	}
	expectedTotal := uint64(16384000) * 1024
	expectedAvail := uint64(8192000) * 1024
	expectedUsed := expectedTotal - expectedAvail
	if total != expectedTotal {
		t.Errorf("expected MemTotal %d, got %d", expectedTotal, total)
	}
	if avail != expectedAvail {
		t.Errorf("expected MemAvail %d, got %d", expectedAvail, avail)
	}
	if used != expectedUsed {
		t.Errorf("expected MemUsed %d, got %d", expectedUsed, used)
	}
	if swapTotal != uint64(4096000)*1024 {
		t.Errorf("expected SwapTotal %d, got %d", uint64(4096000)*1024, swapTotal)
	}
	if swapUsed != 0 {
		t.Errorf("expected SwapUsed 0, got %d", swapUsed)
	}
}

func TestParsePIDStat_ParenthesesAndSpaces(t *testing.T) {
	// Process comm with spaces and parentheses: "(Web Content (1))"
	data := []byte("202 (Web Content (1)) R 1 202 202 0 0 4194304 200 0 0 0 500 100 0 0 20 0 4 0 6000 2147483648 262144 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n")
	res, err := ParsePIDStat(data, 4096)
	if err != nil {
		t.Fatalf("ParsePIDStat failed: %v", err)
	}
	if res.PID != 202 {
		t.Errorf("expected PID 202, got %d", res.PID)
	}
	if res.Comm != "Web Content (1)" {
		t.Errorf("expected Comm 'Web Content (1)', got %q", res.Comm)
	}
	if res.State != 'R' {
		t.Errorf("expected state 'R', got '%c'", res.State)
	}
	if res.PPID != 1 {
		t.Errorf("expected PPID 1, got %d", res.PPID)
	}
	if res.UTimeJiffies != 500 {
		t.Errorf("expected utime 500, got %d", res.UTimeJiffies)
	}
	if res.STimeJiffies != 100 {
		t.Errorf("expected stime 100, got %d", res.STimeJiffies)
	}
	if res.NumThreads != 4 {
		t.Errorf("expected NumThreads 4, got %d", res.NumThreads)
	}
	if res.StartTime != 6000 {
		t.Errorf("expected StartTime 6000, got %d", res.StartTime)
	}
	if res.VMSBytes != 2147483648 {
		t.Errorf("expected VMSBytes 2147483648, got %d", res.VMSBytes)
	}
	expectedRSS := uint64(262144) * 4096 // 1 GiB
	if res.RSSBytes != expectedRSS {
		t.Errorf("expected RSSBytes %d, got %d", expectedRSS, res.RSSBytes)
	}
}

func TestSanitizeString(t *testing.T) {
	// Terminal escape sequence: \x1b[31mhello\x1b[0m
	input := "hello\x1b[31mworld\x7f\x01\x02test"
	sanitized := SanitizeString(input)
	if sanitized != "hello [31mworld   test" {
		t.Errorf("unexpected sanitized string: %q", sanitized)
	}
}

func TestParsePIDCmdline(t *testing.T) {
	raw := []byte("nginx: master process\x00-g\x00daemon off;\x00")
	cmdline := ParsePIDCmdline(raw, "nginx")
	expected := "nginx: master process -g daemon off;"
	if cmdline != expected {
		t.Errorf("expected %q, got %q", expected, cmdline)
	}

	// Empty cmdline should fallback to [comm]
	empty := ParsePIDCmdline([]byte{}, "kworker")
	if empty != "[kworker]" {
		t.Errorf("expected '[kworker]', got %q", empty)
	}
}

func TestParseSmapsRollup(t *testing.T) {
	data := []byte(`10000000-10001000 ---p 00000000 00:00 0
Pss:               12288 kB
Pss_Dirty:             0 kB
Pss_Anon:              0 kB
Pss_File:          12288 kB
Pss_Shmem:             0 kB
`)
	pss, found := ParseSmapsRollup(data)
	if !found {
		t.Fatalf("expected Pss to be found")
	}
	expectedBytes := uint64(12288) * 1024
	if pss != expectedBytes {
		t.Errorf("expected Pss %d bytes, got %d", expectedBytes, pss)
	}
}

func TestFSCollector_Tick01(t *testing.T) {
	fixtureFS := os.DirFS("../../testdata/proc_fixtures/tick_01")
	collector := NewFSCollector(fixtureFS, 4096, true)

	sample, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect on tick_01 failed: %v", err)
	}

	if sample.TotalJiffies != 100000 {
		t.Errorf("expected TotalJiffies 100000, got %d", sample.TotalJiffies)
	}
	if sample.ActiveJiffies != 12000 {
		t.Errorf("expected ActiveJiffies 12000, got %d", sample.ActiveJiffies)
	}
	if sample.NumCores != 4 {
		t.Errorf("expected NumCores 4, got %d", sample.NumCores)
	}
	if len(sample.Processes) != 3 {
		t.Fatalf("expected 3 processes in tick_01, got %d", len(sample.Processes))
	}

	// Verify PID 101
	var proc101 *model.RawProcessSample
	for i := range sample.Processes {
		if sample.Processes[i].Key.PID == 101 {
			proc101 = &sample.Processes[i]
			break
		}
	}
	if proc101 == nil {
		t.Fatalf("process 101 not found")
	}
	if proc101.Key.StartTime != 5000 {
		t.Errorf("expected StartTime 5000, got %d", proc101.Key.StartTime)
	}
	if proc101.Comm != "nginx" {
		t.Errorf("expected Comm 'nginx', got %q", proc101.Comm)
	}
	if proc101.UID != 1000 {
		t.Errorf("expected UID 1000, got %d", proc101.UID)
	}
	if !proc101.PSSValid || proc101.PSSBytes != uint64(12288)*1024 {
		t.Errorf("expected PSS 12288 kB, valid=%v, got %d", proc101.PSSValid, proc101.PSSBytes)
	}
}

func TestFSCollector_Malformed(t *testing.T) {
	fixtureFS := os.DirFS("../../testdata/proc_fixtures/malformed")
	collector := NewFSCollector(fixtureFS, 4096, true)

	sample, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect on malformed failed: %v", err)
	}

	// In malformed:
	// self and sys are non-numeric -> skipped
	// 501 has truncated stat -> ParseErrors incremented
	// 502 is valid with sanitized comm and cmdline
	if sample.ParseErrors < 1 {
		t.Errorf("expected ParseErrors >= 1, got %d", sample.ParseErrors)
	}

	var proc502 *model.RawProcessSample
	for i := range sample.Processes {
		if sample.Processes[i].Key.PID == 502 {
			proc502 = &sample.Processes[i]
			break
		}
	}
	if proc502 == nil {
		t.Fatalf("process 502 should be parsed successfully")
	}
	// Verify escape character is sanitized
	for i := 0; i < len(proc502.Comm); i++ {
		if proc502.Comm[i] == 0x1b {
			t.Errorf("escape char was not sanitized in comm: %q", proc502.Comm)
		}
	}
}

// errorInjectingFS wraps an fs.FS and returns artificial errors for specific paths
type errorInjectingFS struct {
	underlying fs.FS
	vanishPath string
	permPath   string
}

func (e *errorInjectingFS) Open(name string) (fs.File, error) {
	if name == e.vanishPath {
		return nil, fs.ErrNotExist
	}
	if name == e.permPath {
		return nil, fs.ErrPermission
	}
	return e.underlying.Open(name)
}

func TestFSCollector_FaultTolerance(t *testing.T) {
	baseFS := os.DirFS("../../testdata/proc_fixtures/tick_01")
	wrapped := &errorInjectingFS{
		underlying: baseFS,
		vanishPath: "101/stat",
		permPath:   "202/stat",
	}

	collector := NewFSCollector(wrapped, 4096, false)
	sample, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect should succeed despite individual process errors: %v", err)
	}

	if sample.SkippedVanished != 1 {
		t.Errorf("expected SkippedVanished == 1, got %d", sample.SkippedVanished)
	}
	if sample.SkippedPermission != 1 {
		t.Errorf("expected SkippedPermission == 1, got %d", sample.SkippedPermission)
	}
}

// generate300ProcsFS generates an in-memory MapFS simulating 300 active processes
func generate300ProcsFS() fs.FS {
	m := make(fstest.MapFS)
	m["stat"] = &fstest.MapFile{
		Data: []byte(`cpu  100000 0 20000 880000 0 0 0 0 0 0
cpu0 25000 0 5000 220000 0 0 0 0 0 0
cpu1 25000 0 5000 220000 0 0 0 0 0 0
cpu2 25000 0 5000 220000 0 0 0 0 0 0
cpu3 25000 0 5000 220000 0 0 0 0 0 0
`),
	}
	m["meminfo"] = &fstest.MapFile{
		Data: []byte(`MemTotal:       16384000 kB
MemFree:         4096000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          3584000 kB
SwapTotal:       4096000 kB
SwapFree:        4096000 kB
`),
	}

	for i := 1; i <= 300; i++ {
		pidStr := fmt.Sprintf("%d", 1000+i)
		statData := fmt.Sprintf("%s (worker-%d) S 1 1000 1000 0 0 4194304 100 0 0 0 %d %d 0 0 20 0 2 0 %d 104857600 5120 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n",
			pidStr, i, i*10, i*5, 10000+i)
		statusData := fmt.Sprintf("Name:\tworker-%d\nUid:\t1000\t1000\t1000\t1000\n", i)
		cmdlineData := fmt.Sprintf("worker-process\x00--id=%d\x00", i)

		m[pidStr+"/stat"] = &fstest.MapFile{Data: []byte(statData)}
		m[pidStr+"/status"] = &fstest.MapFile{Data: []byte(statusData)}
		m[pidStr+"/cmdline"] = &fstest.MapFile{Data: []byte(cmdlineData)}
	}
	return m
}

func BenchmarkCollector_300Procs(b *testing.B) {
	fsys := generate300ProcsFS()
	collector := NewFSCollector(fsys, 4096, false)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sample, err := collector.Collect(ctx)
		if err != nil {
			b.Fatalf("Collect failed: %v", err)
		}
		if len(sample.Processes) != 300 {
			b.Fatalf("expected 300 processes, got %d", len(sample.Processes))
		}
	}
}

func TestGuestAccountingAndMalformedCounters(t *testing.T) {
	total, active, _, err := ParseSystemStat([]byte("cpu 100 20 30 400 10 5 5 0 50 10\n"))
	if err != nil || total != 570 || active != 160 {
		t.Fatalf("guest double counted: %d %d %v", total, active, err)
	}
	if _, _, _, err := ParseSystemStat([]byte("cpu bad 0 0 1\n")); err == nil {
		t.Fatal("accepted invalid counter")
	}
	if _, _, _, _, _, err := ParseMeminfo([]byte("MemTotal: broken kB\n")); err == nil {
		t.Fatal("accepted missing memory total")
	}
	data, err := os.ReadFile("../../testdata/proc_fixtures/tick_01/101/stat")
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the final required field without changing field count.
	close := strings.LastIndexByte(string(data), ')')
	fields := strings.Fields(string(data[close+1:]))
	fields[19] = "invalid"
	if _, err := ParsePIDStat([]byte(string(data[:close+1])+" "+strings.Join(fields, " ")), 4096); err == nil {
		t.Fatal("accepted invalid identity")
	}
}
