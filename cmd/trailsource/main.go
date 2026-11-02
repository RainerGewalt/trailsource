// Command trailsource runs the OPC UA source adapter and verifies journals.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/RainerGewalt/trailsource/internal/config"
	"github.com/RainerGewalt/trailsource/internal/evidence"
	"github.com/RainerGewalt/trailsource/internal/opcua"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func usage() {
	fmt.Fprintf(os.Stderr, `trailsource %s

Usage:
  trailsource run --config <file>          run the OPC UA adapter
  trailsource verify [--json] <file>...    verify JSON Lines journals
  trailsource config-version --config <f>  print config_version of a config file
  trailsource version                      print the build version
`, version)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(runCmd(os.Args[2:]))
	case "verify":
		os.Exit(verifyCmd(os.Args[2:]))
	case "config-version":
		os.Exit(configVersionCmd(os.Args[2:]))
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	path := fs.String("config", "", "path to the YAML configuration")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "run: --config is required")
		return 2
	}
	cfg, missing, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	for _, m := range missing {
		log.Printf("config: environment variable %s is not set, expanded to empty string", m)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := opcua.Run(ctx, cfg, version); err != nil {
		fmt.Fprintf(os.Stderr, "adapter: %v\n", err)
		return 1
	}
	return 0
}

func configVersionCmd(args []string) int {
	fs := flag.NewFlagSet("config-version", flag.ContinueOnError)
	path := fs.String("config", "", "path to the YAML configuration")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "config-version: --config is required")
		return 2
	}
	cfg, _, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	fmt.Println(cfg.Version)
	return 0
}

func verifyCmd(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	files := fs.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "verify: at least one journal file is required (use - for stdin)")
		return 2
	}
	exit := 0
	for _, f := range files {
		var r io.Reader
		if f == "-" {
			r = os.Stdin
		} else {
			fh, err := os.Open(f)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
				exit = 1
				continue
			}
			r = fh
		}
		report, err := evidence.VerifyJournal(r)
		if c, isCloser := r.(io.Closer); isCloser {
			_ = c.Close()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			exit = 1
			continue
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"file": f, "ok": report.OK(), "report": report})
		} else {
			printReport(f, report)
		}
		if !report.OK() {
			exit = 1
		}
	}
	return exit
}

func printReport(file string, rep evidence.Report) {
	state := "OK"
	if !rep.OK() {
		state = "FAIL"
	}
	fmt.Printf("%s: %s, %d records, %d streams\n", file, state, rep.Records, len(rep.Streams))
	for _, s := range rep.Streams {
		fmt.Printf("  stream %-40s records=%d seq=%d..%d restarts=%d gaps=%d breaks=%d\n",
			s.StreamID, s.Records, s.FirstSeq, s.LastSeq, s.Restarts, s.Gaps, s.Breaks)
	}
	for _, i := range rep.Issues {
		level := "info"
		if i.IsBreak() {
			level = "BREAK"
		}
		fmt.Printf("  %-5s %-13s line=%d seq=%d %s\n", level, i.Kind, i.Line, i.Seq, i.Detail)
	}
}
