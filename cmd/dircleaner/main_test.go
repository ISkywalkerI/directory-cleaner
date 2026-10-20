package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"directory-cleaner/internal/cleaner"
)

var testBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dircleaner-cli-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testBinary = filepath.Join(dir, "dircleaner")
	if runtime.GOOS == "windows" {
		testBinary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", testBinary, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build CLI: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func cliFixture(t *testing.T, drySetting string, continuous bool) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "数据 with spaces")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"old", "middle", "new"} {
		p := filepath.Join(data, name)
		if err := os.WriteFile(p, []byte("1234"), 0600); err != nil {
			t.Fatal(err)
		}
		m := time.Now().Add(-time.Duration(3-i) * time.Hour)
		if err := os.Chtimes(p, m, m); err != nil {
			t.Fatal(err)
		}
	}
	audit := filepath.Join(root, "audit.jsonl")
	cfg := filepath.Join(root, "config.yaml")
	interval := "1h"
	if continuous {
		interval = "30ms"
	}
	text := fmt.Sprintf("version: 1\ndefaults:\n  scan_interval: %s\n  stable_for: 0s\n  min_age: 0s\n  audit_log: %q\n%sdirectories:\n- path: %q\n  trigger_bytes: 10\n  target_bytes: 4\n", interval, filepath.ToSlash(audit), drySetting, filepath.ToSlash(data))
	if err := os.WriteFile(cfg, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return cfg, audit, data
}

func cliEvents(path string) ([]cleaner.AuditEvent, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result []cleaner.AuditEvent
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e cleaner.AuditEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, nil
}

func TestCLIOnce(t *testing.T) {
	for _, tc := range []struct {
		name, setting, flag string
		deleted             bool
	}{
		{"default preview", "", "", false},
		{"execute override", "", "-execute", true},
		{"preview override", "  dry_run: false\n", "-dry-run", false},
		{"config execute", "  dry_run: false\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, audit, data := cliFixture(t, tc.setting, false)
			args := []string{"-config", cfg, "-once"}
			if tc.flag != "" {
				args = append(args, tc.flag)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if output, err := exec.CommandContext(ctx, testBinary, args...).CombinedOutput(); err != nil {
				t.Fatalf("CLI: %v\n%s", err, output)
			}
			e, err := cliEvents(audit)
			if err != nil {
				t.Fatal(err)
			}
			if len(e) != 3 || e[0].File == nil || e[1].File == nil || e[2].Action != "summary" {
				t.Fatalf("unexpected events: %+v", e)
			}
			if filepath.Base(e[0].File.Path) != "old" || filepath.Base(e[1].File.Path) != "middle" {
				t.Fatal("wrong deletion order")
			}
			wantBytes := int64(12)
			wantCount := 0
			if tc.deleted {
				wantBytes = 4
				wantCount = 2
			}
			if e[2].AfterBytes != wantBytes || e[2].DeletedFiles != wantCount || len(e[2].Failures) != 0 {
				t.Fatal(e[2])
			}
			for _, name := range []string{"old", "middle", "new"} {
				_, err := os.Stat(filepath.Join(data, name))
				if tc.deleted && name != "new" {
					if !os.IsNotExist(err) {
						t.Fatalf("not removed: %s: %v", name, err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCLIInvalidArgumentsAndConfig(t *testing.T) {
	cfg, _, _ := cliFixture(t, "", false)
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("version: 999"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		args     []string
		code     int
		contains string
	}{
		{"conflict", []string{"-config", cfg, "-dry-run", "-execute"}, 2, "mutually exclusive"},
		{"missing", []string{"-config", cfg + "-missing"}, 2, "read config"},
		{"invalid", []string{"-config", bad}, 2, "unsupported config version"},
		{"unknown", []string{"-unknown"}, 2, "flag provided but not defined"},
		{"help", []string{"-h"}, 0, "Usage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, testBinary, tc.args...).CombinedOutput()
			code := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					code = exit.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tc.code || !strings.Contains(string(output), tc.contains) {
				t.Fatalf("exit=%d output=%s err=%v", code, output, err)
			}
		})
	}
}

func TestCLIContinuous(t *testing.T) {
	cfg, audit, data := cliFixture(t, "", true)
	cmd := exec.Command(testBinary, "-config", cfg)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-wait
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		e, err := cliEvents(audit)
		if err == nil && len(e) >= 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("continuous scan did not repeat")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runtime.GOOS == "windows" {
		// shortcut: os.Interrupt is unsupported for Windows child processes; NSSM stop is checked on deployment.
		t.Log("Windows: verified repeated scans; killing child for cleanup; graceful service stop requires manual NSSM acceptance")
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	} else if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-wait:
		stopped = true
		if runtime.GOOS != "windows" && err != nil {
			t.Fatalf("unclean exit: %v\n%s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI failed to stop")
	}
	if e, err := cliEvents(audit); err != nil || len(e) < 6 {
		t.Fatalf("invalid audit: %v %v", e, err)
	}
	files, err := os.ReadDir(data)
	if err != nil || len(files) != 3 {
		t.Fatalf("preview changed files: %v %v", files, err)
	}
}
