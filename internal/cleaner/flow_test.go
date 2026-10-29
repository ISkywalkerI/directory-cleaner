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
		name        string
		trigger     int64
		target      int64
		dry         bool
		sizes       []int
		deletes     int
		wantAfter   int64
		wantDeleted int64
	}{
		{name: "below", trigger: 20, target: 4, sizes: []int{4, 4, 4}, wantAfter: 12},
		{name: "equal", trigger: 12, target: 4, sizes: []int{4, 4, 4}, wantAfter: 12},
		{name: "execute", trigger: 10, target: 4, sizes: []int{4, 4, 4}, deletes: 2, wantAfter: 4, wantDeleted: 8},
		{name: "preview", trigger: 10, target: 4, dry: true, sizes: []int{4, 4, 4}, deletes: 2, wantAfter: 12},
		{name: "uneven sizes stop below target", trigger: 10, target: 4, sizes: []int{7, 3, 2}, deletes: 2, wantAfter: 2, wantDeleted: 10},
		{name: "zero target", trigger: 1, target: 0, sizes: []int{4, 3}, deletes: 2, wantAfter: 0, wantDeleted: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "data")
			candidates := make([]fileCandidate, len(tc.sizes))
			for i, size := range tc.sizes {
				name := fmt.Sprintf("file-%d", i)
				candidates[i] = fixtureFile(t, data, name, time.Duration(len(tc.sizes)-i)*time.Hour)
				if err := os.WriteFile(candidates[i].Path, bytes.Repeat([]byte{'x'}, size), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(candidates[i].Path, candidates[i].ModTime, candidates[i].ModTime); err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(candidates[i].Path)
				if err != nil {
					t.Fatal(err)
				}
				candidates[i].Size = info.Size()
				candidates[i].ModTime = info.ModTime()
			}
			logDir := filepath.Join(root, "logs")
			logger, err := NewLogger(logDir, 0)
			if err != nil {
				t.Fatal(err)
			}
			NewScanner(logger).RunOnce(context.Background(), config.ResolvedDirectory{Name: "data", Path: data, TriggerBytes: tc.trigger, TargetBytes: tc.target, MaxWorkers: 2, DryRun: tc.dry})
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(logDir, "dir-cleaner-*.jsonl"))
			if err != nil || len(files) != 1 {
				t.Fatalf("unexpected log files: %v %v", files, err)
			}
			events := readEvents(t, files[0])
			last := events[len(events)-1]
			var total int64
			for _, size := range tc.sizes {
				total += int64(size)
			}
			if last.BeforeBytes != total || len(last.Failures) != 0 || len(events) != tc.deletes+1 {
				t.Fatalf("unexpected audit: %+v", events)
			}
			if tc.deletes == 0 {
				if last.Action != "scan" {
					t.Fatal(last)
				}
			} else {
				if last.Action != "summary" {
					t.Fatalf("wrong deletion order: %+v", events)
				}
				for i := 0; i < tc.deletes; i++ {
					if events[i].File.Path != candidates[i].Path {
						t.Fatalf("wrong deletion order: %+v", events)
					}
				}
			}
			wantDeletedFiles := tc.deletes
			if tc.dry {
				wantDeletedFiles = 0
			}
			if last.AfterBytes != tc.wantAfter || last.DeletedFiles != wantDeletedFiles || last.DeletedBytes != tc.wantDeleted {
				t.Fatalf("wrong counts: %+v", last)
			}
			if tc.deletes > 0 {
				if last.CurrentBytes == nil || *last.CurrentBytes != total {
					t.Fatalf("wrong current bytes: %+v", last)
				}
				if last.PlannedFiles == nil || *last.PlannedFiles != tc.deletes {
					t.Fatalf("wrong planned file count: %+v", last)
				}
				var plannedBytes int64
				for i := 0; i < tc.deletes; i++ {
					plannedBytes += int64(tc.sizes[i])
				}
				if last.PlannedBytes == nil || *last.PlannedBytes != plannedBytes {
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
			for i, f := range candidates {
				_, err := os.Stat(f.Path)
				shouldExist := tc.dry || i >= tc.deletes
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

func TestScanReportsChangedCandidate(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	old := fixtureFile(t, data, "old", 3*time.Hour)
	middle := fixtureFile(t, data, "middle", 2*time.Hour)
	newest := fixtureFile(t, data, "new", time.Hour)
	logDir := filepath.Join(root, "logs")
	logger, err := NewLogger(logDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		changed <- os.WriteFile(old.Path, []byte("changed size"), 0600)
	}()
	NewScanner(logger).RunOnce(context.Background(), config.ResolvedDirectory{Name: "data", Path: data, TriggerBytes: 1, TargetBytes: 4, StableFor: 500 * time.Millisecond, MaxWorkers: 1})
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(logDir, "dir-cleaner-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("unexpected log files: %v %v", files, err)
	}
	events := readEvents(t, files[0])
	if len(events) != 3 || events[0].Action != "delete" || events[0].File == nil || events[0].File.Path != middle.Path || events[1].Action != "delete" || events[1].File == nil || events[1].File.Path != newest.Path || events[2].Action != "summary" {
		t.Fatalf("unexpected events: %+v", events)
	}
	summary := events[2]
	if summary.DeletedFiles != 2 || summary.DeletedBytes != 8 || summary.AfterBytes != 4 || len(summary.Failures) != 1 || !strings.Contains(summary.Failures[0], old.Path) || !strings.Contains(summary.Failures[0], "changed_during_observation") {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.PlannedFiles == nil || *summary.PlannedFiles != 2 || summary.PlannedBytes == nil || *summary.PlannedBytes != 8 {
		t.Fatalf("unstable candidate counted in plan: %+v", summary)
	}
	for _, path := range []string{old.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file to remain %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "middle")); !os.IsNotExist(err) {
		t.Fatalf("expected middle file to be deleted: %v", err)
	}
	if _, err := os.Stat(newest.Path); !os.IsNotExist(err) {
		t.Fatalf("expected newest file to be deleted: %v", err)
	}
}

func TestScanPlannedCountsRequireFinalValidation(t *testing.T) {
	for _, tc := range []struct {
		name              string
		dryRun            bool
		validatedOnError  bool
		wantPlannedFiles  int
		wantPlannedBytes  int64
		wantDeletedFiles  int
		wantDeletedBytes  int64
		wantAfterBytes    int64
		wantDeleteEvents  int
		wantFirstPlanFile int
	}{
		{name: "final validation failed", wantPlannedFiles: 1, wantPlannedBytes: 4, wantDeletedFiles: 1, wantDeletedBytes: 4, wantAfterBytes: 8, wantDeleteEvents: 1, wantFirstPlanFile: 1},
		{name: "dry run final validation failed", dryRun: true, wantPlannedFiles: 1, wantPlannedBytes: 4, wantAfterBytes: 12, wantDeleteEvents: 1, wantFirstPlanFile: 1},
		{name: "delete failed after validation", validatedOnError: true, wantPlannedFiles: 2, wantPlannedBytes: 8, wantDeletedFiles: 1, wantDeletedBytes: 4, wantAfterBytes: 8, wantDeleteEvents: 1, wantFirstPlanFile: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "data")
			candidates := []fileCandidate{
				fixtureFile(t, data, "old", 3*time.Hour),
				fixtureFile(t, data, "middle", 2*time.Hour),
				fixtureFile(t, data, "new", time.Hour),
			}
			logger, err := NewLogger(filepath.Join(root, "logs"), 0)
			if err != nil {
				t.Fatal(err)
			}
			scanner := NewScanner(logger)
			scanner.removeCandidate = func(path string, size int64, modTime time.Time, dryRun bool) (removalResult, error) {
				if path == candidates[0].Path {
					if tc.validatedOnError {
						return removalResult{validated: true}, fmt.Errorf("remove denied")
					}
					return removalResult{}, fmt.Errorf("changed_before_delete")
				}
				return remove(path, size, modTime, dryRun)
			}
			scanner.RunOnce(context.Background(), config.ResolvedDirectory{
				Name: "data", Path: data, TriggerBytes: 1, TargetBytes: 8,
				MaxWorkers: 1, DryRun: tc.dryRun,
			})
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(root, "logs", "dir-cleaner-*.jsonl"))
			if err != nil || len(files) != 1 {
				t.Fatalf("unexpected log files: %v %v", files, err)
			}
			events := readEvents(t, files[0])
			summary := events[len(events)-1]
			if summary.Action != "summary" || len(events) != tc.wantDeleteEvents+1 {
				t.Fatalf("unexpected events: %+v", events)
			}
			if summary.PlannedFiles == nil || *summary.PlannedFiles != tc.wantPlannedFiles || summary.PlannedBytes == nil || *summary.PlannedBytes != tc.wantPlannedBytes {
				t.Fatalf("wrong planned counts: %+v", summary)
			}
			if summary.DeletedFiles != tc.wantDeletedFiles || summary.DeletedBytes != tc.wantDeletedBytes || summary.AfterBytes != tc.wantAfterBytes {
				t.Fatalf("wrong actual counts: %+v", summary)
			}
			if summary.PlannedFrom == nil || !summary.PlannedFrom.Equal(candidates[tc.wantFirstPlanFile].ModTime) || summary.PlannedTo == nil || !summary.PlannedTo.Equal(candidates[tc.wantPlannedFiles+tc.wantFirstPlanFile-1].ModTime) {
				t.Fatalf("wrong planned time range: %+v", summary)
			}
			if len(summary.Failures) != 1 || !strings.Contains(summary.Failures[0], candidates[0].Path) {
				t.Fatalf("expected first candidate failure: %+v", summary)
			}
			for i, candidate := range candidates {
				_, statErr := os.Stat(candidate.Path)
				shouldExist := tc.dryRun || i == 0 || i == 2
				if shouldExist && statErr != nil {
					t.Fatalf("expected file to remain %q: %v", candidate.Path, statErr)
				}
				if !shouldExist && !os.IsNotExist(statErr) {
					t.Fatalf("expected file to be deleted %q: %v", candidate.Path, statErr)
				}
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
	if result, err := remove(f.Path, f.Size, f.ModTime, true); !result.validated || result.deleted || err != nil {
		t.Fatalf("%+v %v", result, err)
	}
	if err := os.WriteFile(f.Path, []byte("changed size"), 0600); err != nil {
		t.Fatal(err)
	}
	if ok, reason := stable(context.Background(), f.Path, f.Size, f.ModTime, 0); ok || reason != "changed_during_observation" {
		t.Fatalf("%v %s", ok, reason)
	}
	if result, err := remove(f.Path, f.Size, f.ModTime, false); result.validated || result.deleted || err == nil || err.Error() != "changed_before_delete" {
		t.Fatalf("%+v %v", result, err)
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
	if result, err := remove(f.Path, f.Size, f.ModTime, false); result.validated || result.deleted || err == nil {
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

func TestRunIndexedCancellationReturnsFirstUnsubmittedIndex(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	dispatched := make([]int, 0, 3)
	done := make(chan int, 1)
	go func() {
		done <- runIndexed(ctx, 3, 1, func(index int) {
			dispatched = append(dispatched, index)
			if index == 0 {
				close(started)
				<-release
			}
		})
	}()
	<-started
	cancel()
	close(release)
	next := <-done
	if next == 0 || next > 3 || len(dispatched) != next {
		t.Fatalf("first unsubmitted index = %d, dispatched = %v", next, dispatched)
	}
	for index, dispatchedIndex := range dispatched {
		if dispatchedIndex != index {
			t.Fatalf("dispatched indexes = %v", dispatched)
		}
	}
}

func TestCheckStabilityCancellationDuringStableFor(t *testing.T) {
	root := t.TempDir()
	candidates := []fileCandidate{
		fixtureFile(t, root, "first", time.Hour),
		fixtureFile(t, root, "second", time.Hour),
	}
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer func() {
		timer.Stop()
		cancel()
	}()
	results := checkStability(ctx, candidates, config.ResolvedDirectory{
		StableFor:  time.Hour,
		MinAge:     0,
		MaxWorkers: 1,
	})
	for index, result := range results {
		if result.ready || result.reason != "cancelled" {
			t.Fatalf("candidate %d was not cancelled: %+v", index, result)
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
	if result, err := remove(fileLink, target.Size, target.ModTime, false); result.validated || result.deleted || err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("%+v %v", result, err)
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
