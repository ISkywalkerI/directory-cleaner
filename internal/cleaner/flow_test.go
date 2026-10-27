package cleaner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"directory-cleaner/internal/config"
)

func fixtureFile(t *testing.T, root, name string, age time.Duration) fileCandidate {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	m := time.Now().Add(-age)
	if err := os.Chtimes(p, m, m); err != nil {
		t.Fatal(err)
	}
	i, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fileCandidate{FileRecord{p, i.Size(), i.ModTime()}}
}

func readEvents(t *testing.T, path string) []AuditEvent {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []AuditEvent
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e AuditEvent
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("invalid JSONL: %s: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

func TestScanThresholdOrderAndDryRun(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger int64
		dry     bool
		deletes int
	}{
		{"below", 20, false, 0}, {"equal", 12, false, 0}, {"execute", 10, false, 2}, {"preview", 10, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "data")
			old := fixtureFile(t, data, "old", 3*time.Hour)
			middle := fixtureFile(t, data, "nested/middle", 2*time.Hour)
			newest := fixtureFile(t, data, "new", time.Hour)
			logDir := filepath.Join(root, "logs")
			logger, err := NewLogger(logDir, 0)
			if err != nil {
				t.Fatal(err)
			}
			NewScanner(logger).RunOnce(context.Background(), config.ResolvedDirectory{Name: "data", Path: data, TriggerBytes: tc.trigger, TargetBytes: 4, MaxWorkers: 2, DryRun: tc.dry})
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(logDir, "dir-cleaner-*.jsonl"))
			if err != nil || len(files) != 1 {
				t.Fatalf("unexpected log files: %v %v", files, err)
			}
			events := readEvents(t, files[0])
			last := events[len(events)-1]
			if last.BeforeBytes != 12 || len(last.Failures) != 0 || len(events) != tc.deletes+1 {
				t.Fatalf("unexpected audit: %+v", events)
			}
			if tc.deletes == 0 {
				if last.Action != "scan" {
					t.Fatal(last)
				}
			} else {
				if last.Action != "summary" || events[0].File.Path != old.Path || events[1].File.Path != middle.Path {
					t.Fatalf("wrong deletion order: %+v", events)
				}
			}
			wantAfter := int64(12)
			wantDeleted := 0
			if !tc.dry {
				wantAfter -= int64(tc.deletes * 4)
				wantDeleted = tc.deletes
			}
			if last.AfterBytes != wantAfter || last.DeletedFiles != wantDeleted || last.DeletedBytes != int64(wantDeleted*4) {
				t.Fatalf("wrong counts: %+v", last)
			}
			if tc.deletes > 0 {
				if last.CurrentBytes == nil || *last.CurrentBytes != 12 {
					t.Fatalf("wrong current bytes: %+v", last)
				}
				if last.PlannedFiles == nil || *last.PlannedFiles != tc.deletes {
					t.Fatalf("wrong planned file count: %+v", last)
				}
				if last.PlannedBytes == nil || *last.PlannedBytes != int64(tc.deletes*4) {
					t.Fatalf("wrong planned bytes: %+v", last)
				}
				if last.PlannedFrom == nil || last.PlannedTo == nil || last.PlannedFrom.After(*last.PlannedTo) {
					t.Fatalf("wrong planned time range: %+v", last)
				}
				if tc.dry {
					if last.DeletedFrom != nil || last.DeletedTo != nil {
						t.Fatalf("dry run reported deleted time range: %+v", last)
					}
				} else if last.DeletedFrom == nil || last.DeletedTo == nil || last.DeletedFrom.After(*last.DeletedTo) {
					t.Fatalf("wrong deleted time range: %+v", last)
				}
			}
			for _, f := range []fileCandidate{old, middle, newest} {
				_, err := os.Stat(f.Path)
				shouldExist := tc.dry || tc.deletes == 0 || f.Path == newest.Path
				if shouldExist && err != nil {
					t.Fatal(err)
				}
				if !shouldExist && !os.IsNotExist(err) {
					t.Fatalf("expected removed: %s: %v", f.Path, err)
				}
			}
			if tc.dry && tc.deletes > 0 && (events[0].Reason != "dry_run" || !events[0].DryRun) {
				t.Fatal(events[0])
			}
		})
	}
}

func TestCollectNestedAndMissing(t *testing.T) {
	root := t.TempDir()
	fixtureFile(t, root, "a", time.Hour)
	fixtureFile(t, root, "子目录 with spaces/b", time.Hour)
	files, total, failures := collect(root)
	if len(files) != 2 || total != 8 || len(failures) != 0 {
		t.Fatalf("%v %d %v", files, total, failures)
	}
	_, _, failures = collect(filepath.Join(root, "absent"))
	if len(failures) == 0 {
		t.Fatal("missing directory not reported")
	}
}

func TestAgeAndCancelledScan(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "young", true: "cancelled"}[cancelled], func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "data")
			f := fixtureFile(t, data, "keep", 0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			p := filepath.Join(root, "logs")
			l, err := NewLogger(p, 0)
			if err != nil {
				t.Fatal(err)
			}
			NewScanner(l).RunOnce(ctx, config.ResolvedDirectory{Path: data, TriggerBytes: 1, TargetBytes: 0, MinAge: time.Hour, MaxWorkers: 1})
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(p, "dir-cleaner-*.jsonl"))
			if err != nil || len(files) != 1 {
				t.Fatalf("unexpected log files: %v %v", files, err)
			}
			e := readEvents(t, files[0])
			if len(e) != 1 || len(e[0].Failures) == 0 || e[0].DeletedFiles != 0 {
				t.Fatal(e)
			}
			if _, err := os.Stat(f.Path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStableAndRemoveRecheck(t *testing.T) {
	f := fixtureFile(t, t.TempDir(), "file", time.Hour)
	if ok, reason := stable(context.Background(), f.Path, f.Size, f.ModTime, 0); !ok {
		t.Fatal(reason)
	}
	if ok, err := remove(f.Path, f.Size, f.ModTime, true); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if err := os.WriteFile(f.Path, []byte("changed size"), 0600); err != nil {
		t.Fatal(err)
	}
	if ok, reason := stable(context.Background(), f.Path, f.Size, f.ModTime, 0); ok || reason != "changed_during_observation" {
		t.Fatalf("%v %s", ok, reason)
	}
	if ok, err := remove(f.Path, f.Size, f.ModTime, false); ok || err == nil || err.Error() != "changed_before_delete" {
		t.Fatalf("%v %v", ok, err)
	}
	if _, err := os.Stat(f.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.Path); err != nil {
		t.Fatal(err)
	}
	if ok, _ := stable(context.Background(), f.Path, f.Size, f.ModTime, 0); ok {
		t.Fatal("missing file stable")
	}
	if ok, err := remove(f.Path, f.Size, f.ModTime, false); ok || err == nil {
		t.Fatal("missing file removed")
	}
}

func TestStableObservation(t *testing.T) {
	f := fixtureFile(t, t.TempDir(), "file", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	ok, reason := stable(ctx, f.Path, f.Size, f.ModTime, time.Hour)
	if ok || reason != "cancelled" || time.Since(start) > time.Second {
		t.Fatalf("%v %s", ok, reason)
	}
	// Change while the observation is in progress; removal must still be refused.
	done := make(chan error, 1)
	go func() { time.Sleep(20 * time.Millisecond); done <- os.WriteFile(f.Path, []byte("changed"), 0600) }()
	ok, reason = stable(context.Background(), f.Path, f.Size, f.ModTime, 100*time.Millisecond)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ok || reason != "changed_during_observation" {
		t.Fatalf("%v %s", ok, reason)
	}
}

func TestCheckStabilityWaitsOncePerBatch(t *testing.T) {
	root := t.TempDir()
	candidates := make([]fileCandidate, 0, 8)
	for i := 0; i < 8; i++ {
		candidates = append(candidates, fixtureFile(t, root, filepath.Join("data", fmt.Sprintf("file-%d", i)), time.Hour))
	}
	start := time.Now()
	results := checkStability(context.Background(), candidates, config.ResolvedDirectory{
		StableFor:  100 * time.Millisecond,
		MinAge:     0,
		MaxWorkers: 1,
	})
	if elapsed := time.Since(start); elapsed >= 600*time.Millisecond {
		t.Fatalf("stability check waited per file: %s", elapsed)
	}
	for index, result := range results {
		if !result.ready || result.reason != "" {
			t.Fatalf("candidate %d was not stable: %+v", index, result)
		}
	}
}

func TestSymlinkDirectoryAndRemove(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := fixtureFile(t, outside, "keep", time.Hour)
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	f, total, fail := collect(root)
	if len(f) != 0 || total != 0 || len(fail) != 0 {
		t.Fatalf("followed symlink: %v %d %v", f, total, fail)
	}
	fileLink := filepath.Join(root, "file-link")
	if err := os.Symlink(target.Path, fileLink); err != nil {
		t.Fatal(err)
	}
	if ok, err := remove(fileLink, target.Size, target.ModTime, false); ok || err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("%v %v", ok, err)
	}
	if _, err := os.Stat(target.Path); err != nil {
		t.Fatal(err)
	}
}

func TestLoggerConcurrentAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested")
	for pass := 0; pass < 2; pass++ {
		l, err := NewLogger(p, 0)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := l.Write(AuditEvent{Action: "scan"}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	files, err := filepath.Glob(filepath.Join(p, "dir-cleaner-*.jsonl"))
	if err != nil || len(files) != 2 {
		t.Fatalf("unexpected log files: %v %v", files, err)
	}
	count := 0
	for _, file := range files {
		count += len(readEvents(t, file))
	}
	if count != 40 {
		t.Fatalf("lost events: %d", count)
	}
}

func TestLogNamesUseSequenceWhenTimestampCollides(t *testing.T) {
	root := t.TempDir()
	stamp := time.Now().Format("20060102-150405")
	base := filepath.Join(root, "dir-cleaner-"+stamp+".jsonl")
	if err := os.WriteFile(base, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(mustNextLogPath(t, root)), "dir-cleaner-"+stamp+"-1.jsonl"; got != want {
		t.Fatalf("first collision name = %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(root, "dir-cleaner-"+stamp+"-1.jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(mustNextLogPath(t, root)), "dir-cleaner-"+stamp+"-2.jsonl"; got != want {
		t.Fatalf("second collision name = %q, want %q", got, want)
	}
}

func mustNextLogPath(t *testing.T, dir string) string {
	t.Helper()
	path, err := nextLogPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoggerRotatesBySize(t *testing.T) {
	root := t.TempDir()
	l, err := NewLogger(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := l.Write(AuditEvent{Action: "scan", Directory: strings.Repeat("x", 60)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "dir-cleaner-*.jsonl"))
	if err != nil || len(files) < 2 {
		t.Fatalf("rotation did not create timestamped file: %v %v", files, err)
	}
}
