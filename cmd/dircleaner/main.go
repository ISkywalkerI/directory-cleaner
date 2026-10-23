package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"

	"directory-cleaner/internal/cleaner"
	"directory-cleaner/internal/config"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	configPath := flag.String("config", "directory-cleaner.yaml", "YAML configuration path")
	once := flag.Bool("once", false, "scan once and exit")
	dryRun := flag.Bool("dry-run", false, "override all directories to dry-run")
	execute := flag.Bool("execute", false, "override all directories to delete")
	flag.Parse()
	if *showVersion {
		fmt.Printf("dircleaner %s commit=%s built=%s platform=%s/%s\n", version, commit, buildTime, runtime.GOOS, runtime.GOARCH)
		return
	}
	if *dryRun && *execute {
		fmt.Fprintln(os.Stderr, "-dry-run and -execute are mutually exclusive")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *dryRun || *execute {
		for i := range cfg.Directories {
			cfg.Directories[i].DryRun = *dryRun
		}
	}
	logger, err := cleaner.NewLogger(cfg.AuditLog)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer logger.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	scanner := cleaner.NewScanner(logger)
	if *once {
		for _, dir := range cfg.Directories {
			scanner.RunOnce(ctx, dir)
		}
		return
	}
	var wg sync.WaitGroup
	for _, dir := range cfg.Directories {
		wg.Add(1)
		go func(d config.ResolvedDirectory) { defer wg.Done(); scanner.Run(ctx, d) }(dir)
	}
	wg.Wait()
}
