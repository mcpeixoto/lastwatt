// Command lastwatt keeps a laptop-as-server alive through a mains outage by
// progressively shedding load, then restores the exact prior state.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/mcpeixoto/lastwatt/internal/policy"
)

const defaultConfig = "/etc/lastwatt/lastwatt.toml"

var version = "dev"

func usage() {
	fmt.Fprint(os.Stderr, `lastwatt - survive power outages on internal battery

usage: lastwatt [--config PATH] <command>

commands:
  run                 run the daemon (this is what the systemd unit calls)
  status              show AC state, battery, draw and current tier
  simulate            dry-run every tier and print exactly what would happen
  shed <tier|all>     manually engage tiers, for testing
  restore             restore everything shed
  report              print the most recent outage report
  doctor              audit this machine for power-management hazards
  version

flags:
  --config PATH       config file (default `+defaultConfig+`)
  --dry-run           with shed/restore: report actions without performing them
`)
}

func main() {
	var cfgPath string
	var dryRun bool
	fs := flag.NewFlagSet("lastwatt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = usage
	fs.StringVar(&cfgPath, "config", defaultConfig, "config file path")
	fs.BoolVar(&dryRun, "dry-run", false, "do not perform actions")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	args := fs.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if args[0] == "version" {
		fmt.Println("lastwatt", version)
		return
	}
	if args[0] == "doctor" {
		os.Exit(doctor())
	}

	cfg, err := policy.Load(cfgPath)
	if err != nil {
		fatalf("config: %v", err)
	}
	tiers, err := cfg.BuildTiers()
	if err != nil {
		fatalf("config: %v", err)
	}

	d, err := newDaemon(cfg, tiers)
	if err != nil {
		fatalf("%v", err)
	}
	d.dryRun = dryRun

	switch args[0] {
	case "run":
		if err := d.Run(ctx); err != nil && ctx.Err() == nil {
			fatalf("%v", err)
		}
	case "status":
		if err := d.PrintStatus(os.Stdout); err != nil {
			fatalf("%v", err)
		}
	case "simulate":
		if err := d.Simulate(ctx, os.Stdout); err != nil {
			fatalf("%v", err)
		}
	case "shed":
		if len(args) < 2 {
			fatalf("shed needs a tier number or 'all'")
		}
		level := len(tiers)
		if args[1] != "all" {
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 || n > len(tiers) {
				fatalf("tier must be 1-%d or 'all'", len(tiers))
			}
			level = n
		}
		if err := d.ShedTo(ctx, level, "manual"); err != nil {
			fatalf("%v", err)
		}
	case "restore":
		if err := d.RestoreTo(ctx, 0); err != nil {
			fatalf("%v", err)
		}
	case "report":
		if err := d.PrintLastReport(os.Stdout); err != nil {
			fatalf("%v", err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "lastwatt: "+format+"\n", a...)
	os.Exit(1)
}
