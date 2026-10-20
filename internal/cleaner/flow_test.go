package cleaner

import (
	"bytes"
	"context"
	"encoding/json"
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
			logPath := filepath.Join(root, "audit.jsonl")
			logger, err := NewLogger(logPath)
			if err != nil {
				t.Fatal(err)
			}
			NewScanner(logger).RunOnce(context.Background(), config.ResolvedDirectory{Name: "data", Path: data, TriggerBytes: tc.trigger, TargetBytes: 4, MaxWorkers: 2, DryRun: tc.dry})
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			events := readEvents(t, logPath)
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
			p := filepath.Join(root, "audit.jsonl")
			l, err := NewLogger(p)
			if err != nil {
				t.Fatal(err)
			}
			NewScanner(l).RunOnce(ctx, config.ResolvedDirectory{Path: data, TriggerBytes: 1, TargetBytes: 0, MinAge: time.Hour, MaxWorkers: 1})
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			e := readEvents(t, p)
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
	p := filepath.Join(t.TempDir(), "nested", "audit.jsonl")
	for pass := 0; pass < 2; pass++ {
		l, err := NewLogger(p)
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
	if events := readEvents(t, p); len(events) != 40 {
		t.Fatalf("lost events: %d", len(events))
	}
	if _, err := NewLogger(filepath.Dir(p)); err == nil {
		t.Fatal("directory accepted as log file")
	}
}
