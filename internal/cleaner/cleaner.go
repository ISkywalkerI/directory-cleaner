package cleaner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"directory-cleaner/internal/config"
)

type FileRecord struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}
type AuditEvent struct {
	Time         time.Time   `json:"time"`
	Directory    string      `json:"directory"`
	Path         string      `json:"path"`
	DryRun       bool        `json:"dry_run"`
	BeforeBytes  int64       `json:"before_bytes"`
	AfterBytes   int64       `json:"after_bytes"`
	DeletedBytes int64       `json:"deleted_bytes"`
	DeletedFiles int         `json:"deleted_files"`
	SkippedFiles int         `json:"skipped_files"`
	Failures     []string    `json:"failures,omitempty"`
	Action       string      `json:"action"`
	File         *FileRecord `json:"file,omitempty"`
	Reason       string      `json:"reason,omitempty"`
	CurrentBytes *int64      `json:"current_bytes,omitempty"`
	PlannedFiles *int        `json:"planned_files,omitempty"`
	PlannedBytes *int64      `json:"planned_bytes,omitempty"`
	PlannedFrom  *time.Time  `json:"planned_mod_time_from,omitempty"`
	PlannedTo    *time.Time  `json:"planned_mod_time_to,omitempty"`
	DeletedFrom  *time.Time  `json:"deleted_mod_time_from,omitempty"`
	DeletedTo    *time.Time  `json:"deleted_mod_time_to,omitempty"`
}

type Logger struct {
	mu       sync.Mutex
	file     *os.File
	writer   *bufio.Writer
	dir      string
	maxBytes int64
}

func NewLogger(dir string, maxBytes int64) (*Logger, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("audit_log_max_bytes must be non-negative")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	path, err := nextLogPath(dir)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	writer := bufio.NewWriter(file)
	return &Logger{file: file, writer: writer, dir: dir, maxBytes: maxBytes}, nil
}
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.writer.Flush(); err != nil {
		_ = l.file.Close()
		return err
	}
	return l.file.Close()
}
func (l *Logger) Write(event AuditEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if l.maxBytes > 0 {
		info, err := l.file.Stat()
		if err != nil {
			return err
		}
		if info.Size() > 0 && info.Size()+int64(len(data)) >= l.maxBytes {
			if err := l.rotate(); err != nil {
				return err
			}
		}
	}
	if _, err := l.writer.Write(data); err != nil {
		return err
	}
	return l.writer.Flush()
}

func (l *Logger) rotate() error {
	if err := l.writer.Flush(); err != nil {
		return err
	}
	path, err := nextLogPath(l.dir)
	if err != nil {
		return err
	}
	if err := l.file.Close(); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	l.file = file
	l.writer = bufio.NewWriter(file)
	return nil
}

func nextLogPath(dir string) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	for index := 0; ; index++ {
		name := fmt.Sprintf("dir-cleaner-%s.jsonl", stamp)
		if index > 0 {
			name = fmt.Sprintf("dir-cleaner-%s-%d.jsonl", stamp, index)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", fmt.Errorf("check audit log path: %w", err)
		}
	}
}

type fileCandidate struct{ FileRecord }
type stabilityResult struct {
	ready  bool
	reason string
}
type removalResult struct {
	validated bool
	deleted   bool
}
type Scanner struct {
	logger          *Logger
	removeCandidate func(string, int64, time.Time, bool) (removalResult, error)
}

func NewScanner(logger *Logger) *Scanner {
	return &Scanner{logger: logger, removeCandidate: remove}
}
func (s *Scanner) Run(ctx context.Context, dir config.ResolvedDirectory) {
	ticker := time.NewTicker(dir.ScanInterval)
	defer ticker.Stop()
	s.scan(ctx, dir)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx, dir)
		}
	}
}
func (s *Scanner) RunOnce(ctx context.Context, dir config.ResolvedDirectory) { s.scan(ctx, dir) }

func (s *Scanner) scan(ctx context.Context, dir config.ResolvedDirectory) {
	start := time.Now()
	candidates, total, failures := collect(dir.Path)
	event := AuditEvent{Time: start, Directory: dir.Name, Path: dir.Path, DryRun: dir.DryRun, BeforeBytes: total, AfterBytes: total, Failures: failures, Action: "scan"}
	if total <= dir.TriggerBytes {
		_ = s.logger.Write(event)
		return
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ModTime.Before(candidates[j].ModTime) })
	stability := checkStability(ctx, candidates, dir)
	remaining := total
	plannedFiles := 0
	var plannedBytes int64
	var plannedFrom, plannedTo *time.Time
	var deletedFrom, deletedTo *time.Time
	for index, candidate := range candidates {
		if remaining <= dir.TargetBytes {
			break
		}
		if ctx.Err() != nil {
			event.Failures = append(event.Failures, "cancelled")
			break
		}
		ready, reason := stability[index].ready, stability[index].reason
		if !ready {
			event.SkippedFiles++
			event.Failures = append(event.Failures, candidate.Path+": "+reason)
			continue
		}
		result, err := s.removeCandidate(candidate.Path, candidate.Size, candidate.ModTime, dir.DryRun)
		if result.validated {
			plannedFiles++
			plannedBytes += candidate.Size
			if plannedFrom == nil || candidate.ModTime.Before(*plannedFrom) {
				modTime := candidate.ModTime
				plannedFrom = &modTime
			}
			if plannedTo == nil || candidate.ModTime.After(*plannedTo) {
				modTime := candidate.ModTime
				plannedTo = &modTime
			}
		}
		if err != nil {
			event.Failures = append(event.Failures, candidate.Path+": "+err.Error())
			continue
		}
		if !dir.DryRun {
			if deletedFrom == nil || candidate.ModTime.Before(*deletedFrom) {
				modTime := candidate.ModTime
				deletedFrom = &modTime
			}
			if deletedTo == nil || candidate.ModTime.After(*deletedTo) {
				modTime := candidate.ModTime
				deletedTo = &modTime
			}
		}
		fileEvent := event
		fileEvent.Action = "delete"
		fileEvent.File = &candidate.FileRecord
		remaining -= candidate.Size
		if dir.DryRun {
			fileEvent.Reason = "dry_run"
		} else {
			event.DeletedBytes += candidate.Size
			event.DeletedFiles++
		}
		if result.deleted || dir.DryRun {
			_ = s.logger.Write(fileEvent)
		}
	}
	if !dir.DryRun {
		event.AfterBytes = remaining
	}
	event.CurrentBytes = &total
	event.PlannedFiles = &plannedFiles
	event.PlannedBytes = &plannedBytes
	event.PlannedFrom = plannedFrom
	event.PlannedTo = plannedTo
	event.DeletedFrom = deletedFrom
	event.DeletedTo = deletedTo
	event.Action = "summary"
	_ = s.logger.Write(event)
}

func checkStability(ctx context.Context, candidates []fileCandidate, dir config.ResolvedDirectory) []stabilityResult {
	results := make([]stabilityResult, len(candidates))
	eligible := make([]int, 0, len(candidates))
	var eligibleMu sync.Mutex
	unsubmitted := runIndexed(ctx, len(candidates), dir.MaxWorkers, func(index int) {
		if ctx.Err() != nil {
			results[index] = stabilityResult{reason: "cancelled"}
			return
		}
		candidate := candidates[index]
		if time.Since(candidate.ModTime) < dir.MinAge {
			results[index] = stabilityResult{reason: "too_young"}
			return
		}
		if err := checkPlatformAccess(candidate.Path); err != nil {
			results[index] = stabilityResult{reason: "locked_or_inaccessible: " + err.Error()}
			return
		}
		eligibleMu.Lock()
		eligible = append(eligible, index)
		eligibleMu.Unlock()
	})
	for index := unsubmitted; index < len(candidates); index++ {
		results[index] = stabilityResult{reason: "cancelled"}
	}
	if ctx.Err() != nil || len(eligible) == 0 {
		return results
	}
	if dir.StableFor > 0 {
		timer := time.NewTimer(dir.StableFor)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			for _, index := range eligible {
				results[index] = stabilityResult{reason: "cancelled"}
			}
			return results
		}
	}

	unsubmitted = runIndexed(ctx, len(eligible), dir.MaxWorkers, func(position int) {
		index := eligible[position]
		if ctx.Err() != nil {
			results[index] = stabilityResult{reason: "cancelled"}
			return
		}
		candidate := candidates[index]
		ready, reason := verifyStable(ctx, candidate.Path, candidate.Size, candidate.ModTime)
		results[index] = stabilityResult{ready: ready, reason: reason}
	})
	for _, index := range eligible[unsubmitted:] {
		results[index] = stabilityResult{reason: "cancelled"}
	}
	return results
}

func runIndexed(ctx context.Context, count, maxWorkers int, fn func(int)) int {
	if count == 0 {
		return 0
	}
	workers := maxWorkers
	if workers > count {
		workers = count
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				fn(index)
			}
		}()
	}
	next := 0
dispatch:
	for next < count {
		select {
		case jobs <- next:
			next++
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	return next
}

func collect(root string) ([]fileCandidate, int64, []string) {
	var files []fileCandidate
	var total int64
	var failures []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			failures = append(failures, path+": "+err.Error())
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			failures = append(failures, path+": "+statErr.Error())
			return nil
		}
		total += info.Size()
		files = append(files, fileCandidate{FileRecord: FileRecord{Path: path, Size: info.Size(), ModTime: info.ModTime()}})
		return nil
	})
	if err != nil {
		failures = append(failures, root+": "+err.Error())
	}
	return files, total, failures
}

func stable(ctx context.Context, path string, expectedSize int64, expectedMod time.Time, wait time.Duration) (bool, string) {
	if err := checkPlatformAccess(path); err != nil {
		return false, "locked_or_inaccessible: " + err.Error()
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return false, "cancelled"
		}
	}
	return verifyStable(ctx, path, expectedSize, expectedMod)
}

func verifyStable(ctx context.Context, path string, expectedSize int64, expectedMod time.Time) (bool, string) {
	if ctx.Err() != nil {
		return false, "cancelled"
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, "stat_failed: " + err.Error()
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, "symlink"
	}
	if info.Size() != expectedSize || !info.ModTime().Equal(expectedMod) {
		return false, "changed_during_observation"
	}
	if err := checkPlatformAccess(path); err != nil {
		return false, "locked_or_inaccessible: " + err.Error()
	}
	return true, ""
}
func remove(path string, expectedSize int64, expectedMod time.Time, dryRun bool) (removalResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return removalResult{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return removalResult{}, fmt.Errorf("symlink")
	}
	if info.Size() != expectedSize || !info.ModTime().Equal(expectedMod) {
		return removalResult{}, fmt.Errorf("changed_before_delete")
	}
	if err := checkPlatformAccess(path); err != nil {
		return removalResult{}, err
	}
	if dryRun {
		return removalResult{validated: true}, nil
	}
	if err := os.Remove(path); err != nil {
		return removalResult{validated: true}, err
	}
	return removalResult{validated: true, deleted: true}, nil
}
