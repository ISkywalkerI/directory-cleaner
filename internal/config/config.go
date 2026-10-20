package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type ByteSize int64

func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	value := strings.ToUpper(strings.TrimSpace(node.Value))
	multiplier := int64(1)
	for suffix, factor := range map[string]int64{"KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
			multiplier = factor
			break
		}
	}
	number, err := strconv.ParseFloat(value, 64)
	bytes := number * float64(multiplier)
	if err != nil || number < 0 || math.IsNaN(bytes) || math.IsInf(bytes, 0) || bytes >= math.Exp2(63) {
		return fmt.Errorf("invalid byte size %q", node.Value)
	}
	*b = ByteSize(bytes)
	return nil
}

type Config struct {
	Version     int         `yaml:"version"`
	Defaults    Defaults    `yaml:"defaults"`
	Directories []Directory `yaml:"directories"`
}

type Defaults struct {
	ScanInterval string `yaml:"scan_interval"`
	StableFor    string `yaml:"stable_for"`
	MinAge       string `yaml:"min_age"`
	MaxWorkers   int    `yaml:"max_workers"`
	DryRun       *bool  `yaml:"dry_run"`
	AuditLog     string `yaml:"audit_log"`
}

type Directory struct {
	Name         string   `yaml:"name"`
	Path         string   `yaml:"path"`
	TriggerBytes ByteSize `yaml:"trigger_bytes"`
	TargetBytes  ByteSize `yaml:"target_bytes"`
	ScanInterval string   `yaml:"scan_interval"`
	StableFor    string   `yaml:"stable_for"`
	MinAge       string   `yaml:"min_age"`
	MaxWorkers   int      `yaml:"max_workers"`
	DryRun       *bool    `yaml:"dry_run"`
}

type ResolvedDirectory struct {
	Name         string
	Path         string
	TriggerBytes int64
	TargetBytes  int64
	ScanInterval time.Duration
	StableFor    time.Duration
	MinAge       time.Duration
	MaxWorkers   int
	DryRun       bool
}

type ResolvedConfig struct {
	Directories []ResolvedDirectory
	AuditLog    string
}

func Load(path string) (ResolvedConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf("read config: %w", err)
	}
	var raw Config
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return ResolvedConfig{}, fmt.Errorf("parse config: %w", err)
	}
	if raw.Version != 1 {
		return ResolvedConfig{}, fmt.Errorf("unsupported config version %d", raw.Version)
	}
	if len(raw.Directories) == 0 {
		return ResolvedConfig{}, fmt.Errorf("directories must contain at least one entry")
	}

	defaults := raw.Defaults
	if defaults.ScanInterval == "" {
		defaults.ScanInterval = "5m"
	}
	if defaults.StableFor == "" {
		defaults.StableFor = "2m"
	}
	if defaults.MinAge == "" {
		defaults.MinAge = "10m"
	}
	if defaults.MaxWorkers == 0 {
		defaults.MaxWorkers = 1
	}
	if defaults.AuditLog == "" {
		defaults.AuditLog = "logs/directory-cleaner.jsonl"
	}
	defaultDryRun := true
	if defaults.DryRun != nil {
		defaultDryRun = *defaults.DryRun
	}
	defaultScan, err := time.ParseDuration(defaults.ScanInterval)
	if err != nil || defaultScan <= 0 {
		return ResolvedConfig{}, fmt.Errorf("invalid defaults.scan_interval %q", defaults.ScanInterval)
	}
	defaultStable, err := time.ParseDuration(defaults.StableFor)
	if err != nil || defaultStable < 0 {
		return ResolvedConfig{}, fmt.Errorf("invalid defaults.stable_for %q", defaults.StableFor)
	}
	defaultAge, err := time.ParseDuration(defaults.MinAge)
	if err != nil || defaultAge < 0 {
		return ResolvedConfig{}, fmt.Errorf("invalid defaults.min_age %q", defaults.MinAge)
	}
	if defaults.MaxWorkers < 1 {
		return ResolvedConfig{}, fmt.Errorf("defaults.max_workers must be at least 1")
	}

	resolved := ResolvedConfig{AuditLog: defaults.AuditLog}
	auditPath, err := filepath.Abs(resolved.AuditLog)
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf("resolve audit_log: %w", err)
	}
	seen := make(map[string]bool)
	for i, item := range raw.Directories {
		if item.Name == "" {
			item.Name = fmt.Sprintf("directory-%d", i+1)
		}
		if seen[item.Name] {
			return ResolvedConfig{}, fmt.Errorf("duplicate directory name %q", item.Name)
		}
		seen[item.Name] = true
		if item.Path == "" {
			return ResolvedConfig{}, fmt.Errorf("%s.path is required", item.Name)
		}
		rootPath, err := filepath.Abs(item.Path)
		if err != nil {
			return ResolvedConfig{}, fmt.Errorf("resolve %s.path: %w", item.Name, err)
		}
		rel, err := filepath.Rel(rootPath, auditPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ResolvedConfig{}, fmt.Errorf("audit_log must be outside cleanup directory %s", item.Name)
		}
		if item.TriggerBytes <= 0 || item.TargetBytes < 0 || item.TargetBytes >= item.TriggerBytes {
			return ResolvedConfig{}, fmt.Errorf("%s requires trigger_bytes > target_bytes >= 0", item.Name)
		}
		scan, stable, age := defaultScan, defaultStable, defaultAge
		if item.ScanInterval != "" {
			scan, err = time.ParseDuration(item.ScanInterval)
			if err != nil || scan <= 0 {
				return ResolvedConfig{}, fmt.Errorf("%s.scan_interval is invalid", item.Name)
			}
		}
		if item.StableFor != "" {
			stable, err = time.ParseDuration(item.StableFor)
			if err != nil || stable < 0 {
				return ResolvedConfig{}, fmt.Errorf("%s.stable_for is invalid", item.Name)
			}
		}
		if item.MinAge != "" {
			age, err = time.ParseDuration(item.MinAge)
			if err != nil || age < 0 {
				return ResolvedConfig{}, fmt.Errorf("%s.min_age is invalid", item.Name)
			}
		}
		workers := defaults.MaxWorkers
		if item.MaxWorkers != 0 {
			workers = item.MaxWorkers
		}
		if workers < 1 {
			return ResolvedConfig{}, fmt.Errorf("%s.max_workers must be at least 1", item.Name)
		}
		dryRun := defaultDryRun
		if item.DryRun != nil {
			dryRun = *item.DryRun
		}
		resolved.Directories = append(resolved.Directories, ResolvedDirectory{Name: item.Name, Path: filepath.Clean(item.Path), TriggerBytes: int64(item.TriggerBytes), TargetBytes: int64(item.TargetBytes), ScanInterval: scan, StableFor: stable, MinAge: age, MaxWorkers: workers, DryRun: dryRun})
	}
	return resolved, nil
}
