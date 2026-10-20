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
}

type Logger struct {
	mu      sync.Mutex
	file    *os.File
	writer  *bufio.Writer
	encoder *json.Encoder
}

func NewLogger(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	writer := bufio.NewWriter(file)
	return &Logger{file: file, writer: writer, encoder: json.NewEncoder(writer)}, nil
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
	if err := l.encoder.Encode(event); err != nil {
		return err
	}
	return l.writer.Flush()
}

type fileCandidate struct{ FileRecord }
type stabilityResult struct {
	ready  bool
	reason string
}
type Scanner struct{ logger *Logger }

func NewScanner(logger *Logger) *Scanner { return &Scanner{logger: logger} }
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
		deleted, err := remove(candidate.Path, candidate.Size, candidate.ModTime, dir.DryRun)
		if err != nil {
			event.Failures = append(event.Failures, candidate.Path+": "+err.Error())
			continue
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
		if deleted || dir.DryRun {
			_ = s.logger.Write(fileEvent)
		}
	}
	if !dir.DryRun {
		event.AfterBytes = remaining
	}
	event.Action = "summary"
	_ = s.logger.Write(event)
}

func checkStability(ctx context.Context, candidates []fileCandidate, dir config.ResolvedDirectory) []stabilityResult {
	results := make([]stabilityResult, len(candidates))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := dir.MaxWorkers
	if workers > len(candidates) {
		workers = len(candidates)
	}
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					results[index] = stabilityResult{reason: "cancelled"}
					continue
				}
				candidate := candidates[index]
				if time.Since(candidate.ModTime) < dir.MinAge {
					results[index] = stabilityResult{reason: "too_young"}
					continue
				}
				ready, reason := stable(ctx, candidate.Path, candidate.Size, candidate.ModTime, dir.StableFor)
				results[index] = stabilityResult{ready: ready, reason: reason}
			}
		}()
	}
	for index := range candidates {
		select {
		case jobs <- index:
		case <-ctx.Done():
			results[index] = stabilityResult{reason: "cancelled"}
		}
	}
	close(jobs)
	wg.Wait()
	return results
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
func remove(path string, expectedSize int64, expectedMod time.Time, dryRun bool) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("symlink")
	}
	if info.Size() != expectedSize || !info.ModTime().Equal(expectedMod) {
		return false, fmt.Errorf("changed_before_delete")
	}
	if err := checkPlatformAccess(path); err != nil {
		return false, err
	}
	if dryRun {
		return true, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
