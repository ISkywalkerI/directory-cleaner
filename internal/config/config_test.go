package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestLoadRejectsNonHystereticDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("version: 1\ndirectories:\n- name: data\n  path: data\n  trigger_bytes: 10\n  target_bytes: 10\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected trigger/target validation error")
	}
}

func loadText(t *testing.T, text string) (ResolvedConfig, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

const validDirectory = "version: 1\ndirectories:\n- path: data\n  trigger_bytes: 10\n  target_bytes: 5\n"

func TestLoadDefaultsAndOverrides(t *testing.T) {
	cfg, err := loadText(t, validDirectory)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Directories[0]
	if d.Name != "directory-1" || !d.DryRun || d.ScanInterval != 5*time.Minute || d.StableFor != 2*time.Minute || d.MinAge != 10*time.Minute || d.MaxWorkers != 1 || cfg.AuditLogDir != "logs" || cfg.AuditLogMaxBytes != 10*1024*1024 {
		t.Fatalf("unexpected defaults: %+v %+v", cfg, d)
	}
	cfg, err = loadText(t, `version: 1
defaults:
  scan_interval: 2s
  stable_for: 1s
  min_age: 3s
  max_workers: 2
  dry_run: false
  audit_log_dir: outside
directories:
- name: first
  path: data/../first
  trigger_bytes: 2KB
  target_bytes: 1KB
- name: second
  path: second
  trigger_bytes: 20
  target_bytes: 0
  scan_interval: 4s
  stable_for: 0s
  min_age: 0s
  max_workers: 3
  dry_run: true
`)
	if err != nil {
		t.Fatal(err)
	}
	a, b := cfg.Directories[0], cfg.Directories[1]
	if a.Path != "first" || a.DryRun || a.ScanInterval != 2*time.Second || a.StableFor != time.Second || a.MinAge != 3*time.Second || a.MaxWorkers != 2 || a.TriggerBytes != 2048 || cfg.AuditLogDir != "outside" {
		t.Fatalf("defaults not inherited: %+v", a)
	}
	if !b.DryRun || b.ScanInterval != 4*time.Second || b.StableFor != 0 || b.MinAge != 0 || b.MaxWorkers != 3 {
		t.Fatalf("overrides lost: %+v", b)
	}
	cfg, err = loadText(t, "version: 1\ndefaults:\n  audit_log_max_bytes: 0\ndirectories:\n- path: data\n  trigger_bytes: 10\n  target_bytes: 5\n")
	if err != nil || cfg.AuditLogMaxBytes != 0 {
		t.Fatalf("explicit zero must disable rotation: %+v %v", cfg, err)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	cases := map[string]string{
		"yaml":            "[",
		"version":         strings.Replace(validDirectory, "version: 1", "version: 2", 1),
		"empty":           "version: 1\n",
		"path":            strings.Replace(validDirectory, "path: data", "path: ''", 1),
		"zero trigger":    strings.Replace(validDirectory, "trigger_bytes: 10", "trigger_bytes: 0", 1),
		"negative target": strings.Replace(validDirectory, "target_bytes: 5", "target_bytes: -1", 1),
		"large target":    strings.Replace(validDirectory, "target_bytes: 5", "target_bytes: 20", 1),
		"duplicate":       validDirectory + "- name: directory-1\n  path: second\n  trigger_bytes: 10\n  target_bytes: 0\n",
	}
	for _, field := range []string{"scan_interval", "stable_for", "min_age", "max_workers"} {
		invalid := "-1s"
		if field == "max_workers" {
			invalid = "-1"
		}
		cases["default "+field] = strings.Replace(validDirectory, "directories:", "defaults:\n  "+field+": "+invalid+"\ndirectories:", 1)
		cases["directory "+field] = validDirectory + "  " + field + ": " + invalid + "\n"
	}
	cases["zero interval"] = validDirectory + "  scan_interval: 0s\n"
	cases["invalid duration"] = validDirectory + "  stable_for: invalid\n"
	cases["negative audit log size"] = validDirectory + "\ndefaults:\n  audit_log_max_bytes: -1\n"
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadText(t, input); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("missing config accepted")
	}
}

func TestByteSize(t *testing.T) {
	for input, want := range map[string]int64{"0": 0, "42": 42, "1KB": 1024, "1.5 mb": 1572864, "2gb": 2 << 30, "1TB": 1 << 40} {
		t.Run(input, func(t *testing.T) {
			var got ByteSize
			if err := yaml.Unmarshal([]byte(input), &got); err != nil || int64(got) != want {
				t.Fatalf("got %d, %v; want %d", got, err, want)
			}
		})
	}
	for _, input := range []string{"-1", "garbage", "1XB", "NaN", ".inf", "Inf", "9223372036854775808", "999999999999999999TB"} {
		t.Run(input, func(t *testing.T) {
			var got ByteSize
			if err := yaml.Unmarshal([]byte(input), &got); err == nil {
				t.Fatalf("accepted %s as %d", input, got)
			}
		})
	}
}

func TestLoadAuditLogDir(t *testing.T) {
	root := t.TempDir()
	audit := filepath.Join(root, "logs")
	text := "version: 1\ndefaults:\n  audit_log_dir: '" + filepath.ToSlash(audit) + "'\ndirectories:\n- path: '" + filepath.ToSlash(root) + "'\n  trigger_bytes: 10\n  target_bytes: 0\n"
	cfg, err := loadText(t, text)
	if err != nil {
		t.Fatalf("audit log inside cleanup directory rejected: %v", err)
	}
	if filepath.Clean(cfg.AuditLogDir) != filepath.Clean(audit) || len(cfg.Directories) != 1 {
		t.Fatalf("unexpected resolved config: %+v", cfg)
	}
}

func TestLoadRejectsLegacyAuditLog(t *testing.T) {
	if cfg, err := loadText(t, "version: 1\ndefaults:\n  audit_log: old.jsonl\ndirectories:\n- path: data\n  trigger_bytes: 10\n  target_bytes: 5\n"); err != nil || cfg.AuditLogDir != "logs" {
		t.Fatalf("legacy audit_log should be ignored in favor of default directory: %+v %v", cfg, err)
	}
}
