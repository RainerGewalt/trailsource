// Command trailsource runs the OPC UA source adapter, inspects a server
// before the first run, and verifies and reports on journals.
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
	"strings"
	"syscall"
	"time"

	"github.com/RainerGewalt/trailsource/internal/certgen"
	"github.com/RainerGewalt/trailsource/internal/config"
	"github.com/RainerGewalt/trailsource/internal/evidence"
	"github.com/RainerGewalt/trailsource/internal/mapping"
	"github.com/RainerGewalt/trailsource/internal/opcua"
	"github.com/RainerGewalt/trailsource/internal/report"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func usage() {
	fmt.Fprintf(os.Stderr, `trailsource %s

Usage:
  trailsource run --config <file>                     run the OPC UA adapter
  trailsource verify [--json] <file>...               verify JSON Lines journals
  trailsource report <file> [--html out.html] [--json]
                                                      render a journal as a readable report
  trailsource endpoints <opc.tcp://host:port/path>    list the server's endpoints and security modes
  trailsource browse --config <file> [--json] [--mapping-out <file>]
                                                      browse once (read-only, no MQTT, no journal)
  trailsource gen-cert --uri <urn> [--host <name>]... [--out <dir>]
                                                      create a client certificate for Sign/SignAndEncrypt
  trailsource config-version --config <f>             print config_version of a config file
  trailsource version                                 print the build version
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
	case "report":
		os.Exit(reportCmd(os.Args[2:]))
	case "endpoints":
		os.Exit(endpointsCmd(os.Args[2:]))
	case "browse":
		os.Exit(browseCmd(os.Args[2:]))
	case "gen-cert":
		os.Exit(genCertCmd(os.Args[2:]))
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
	files, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
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
		level := "notice"
		if i.IsBreak() {
			level = "error"
		}
		fmt.Printf("  %-6s %-14s line=%d seq=%d %s\n", level, i.Kind, i.Line, i.Seq, i.Detail)
	}
}

// parseInterleaved parses flags that may appear before or after positional
// arguments ("report journal.jsonl --html out.html").
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func reportCmd(args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	htmlOut := fs.String("html", "", "write a self-contained HTML report to this file")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of text")
	files, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if len(files) != 1 {
		fmt.Fprintln(os.Stderr, "report: exactly one journal file is required")
		return 2
	}
	rep, err := report.FromFile(files[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		return 1
	}
	if *htmlOut != "" {
		f, err := os.Create(*htmlOut)
		if err != nil {
			fmt.Fprintf(os.Stderr, "report: %v\n", err)
			return 1
		}
		if err := rep.WriteHTML(f); err != nil {
			_ = f.Close()
			fmt.Fprintf(os.Stderr, "report: %v\n", err)
			return 1
		}
		if err := f.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "report: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "report: wrote %s\n", *htmlOut)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return 1
		}
		return 0
	}
	if err := rep.WriteText(os.Stdout); err != nil {
		return 1
	}
	return 0
}

func endpointsCmd(args []string) int {
	fs := flag.NewFlagSet("endpoints", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the endpoints as JSON")
	timeout := fs.Duration("timeout", 10*time.Second, "request timeout")
	urls, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "opc.tcp://") {
		fmt.Fprintln(os.Stderr, "endpoints: one opc.tcp:// URL is required")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	eps, err := opcua.Endpoints(ctx, urls[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "endpoints: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(eps)
		return 0
	}
	for _, e := range eps {
		fmt.Printf("%-22s %-15s level=%-3d tokens=%-30s %s\n", e.SecurityPolicy, e.SecurityMode, e.SecurityLevel, strings.Join(e.UserTokens, ","), e.URL)
	}
	if len(eps) > 0 {
		fmt.Printf("server application_uri=%s product_uri=%s\n", eps[0].ApplicationURI, eps[0].ProductURI)
	}
	return 0
}

func browseCmd(args []string) int {
	fs := flag.NewFlagSet("browse", flag.ContinueOnError)
	path := fs.String("config", "", "path to the YAML configuration (source section is used)")
	asJSON := fs.Bool("json", false, "print the inspection as JSON")
	mappingOut := fs.String("mapping-out", "", "write a mapping skeleton for the browsed nodes to this file")
	if _, err := parseInterleaved(fs, args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "browse: --config is required")
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
	ins, err := opcua.Inspect(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browse: %v\n", err)
		return 1
	}
	if *mappingOut != "" {
		raw, err := mapping.Skeleton(ins.Items, cfg.Source.Endpoint, evidence.FormatTime(time.Now()))
		if err == nil {
			err = os.WriteFile(*mappingOut, raw, 0o644)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "browse: mapping skeleton: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "browse: wrote mapping skeleton with %d rules to %s\n", len(ins.Items), *mappingOut)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(ins)
		return 0
	}
	fmt.Printf("endpoint     %s\n", ins.Endpoint)
	fmt.Printf("session      auth=%s policy=%s mode=%s server_uri=%s\n", ins.Session.Auth, ins.Session.SecurityPolicy, ins.Session.SecurityMode, ins.Session.ServerURI)
	fmt.Printf("roots        %s\n", strings.Join(ins.BrowseRoots, ", "))
	fmt.Printf("variables    %d (visited %d nodes with %d requests in %s)\n", len(ins.Items), ins.Visited, ins.Requests, ins.Took)
	if ins.Truncated {
		fmt.Printf("truncated    yes: max_nodes=%d reached, raise it or narrow browse_roots\n", cfg.Source.MaxNodes)
	}
	fmt.Printf("fingerprint  %s\n\n", ins.Fingerprint)
	writable := 0
	for _, it := range ins.Items {
		access := "r "
		if it.UserWritable {
			access = "rw"
			writable++
		}
		fmt.Printf("  %-2s %-12s %-60s %s\n", access, it.DataType, it.BrowsePath, it.NodeID)
	}
	if writable > 0 {
		fmt.Printf("\n%d node(s) are writable for this session. TrailSource never writes; the access level is recorded so a reviewer can see it.\n", writable)
	}
	return 0
}

func genCertCmd(args []string) int {
	fs := flag.NewFlagSet("gen-cert", flag.ContinueOnError)
	uri := fs.String("uri", "", "application URI written into the certificate, e.g. urn:trailsource:host01:extruder-01")
	out := fs.String("out", "pki", "directory for client.der and client.key.pem")
	days := fs.Int("days", 730, "validity in days")
	var hosts multiFlag
	fs.Var(&hosts, "host", "DNS name or IP address for the certificate (repeatable)")
	if _, err := parseInterleaved(fs, args); err != nil {
		return 2
	}
	if *uri == "" {
		fmt.Fprintln(os.Stderr, "gen-cert: --uri is required")
		return 2
	}
	if len(hosts) == 0 {
		if h, err := os.Hostname(); err == nil {
			hosts = append(hosts, h)
		}
	}
	res, err := certgen.Generate(certgen.Options{ApplicationURI: *uri, Hosts: hosts, Days: *days})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-cert: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "gen-cert: %v\n", err)
		return 1
	}
	certPath, keyPath := *out+"/client.der", *out+"/client.key.pem"
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{{certPath, res.CertDER, 0o644}, {keyPath, res.KeyPEM, 0o600}} {
		if _, err := os.Stat(f.path); err == nil {
			fmt.Fprintf(os.Stderr, "gen-cert: %s exists, not overwritten\n", f.path)
			return 1
		}
		if err := os.WriteFile(f.path, f.data, f.mode); err != nil {
			fmt.Fprintf(os.Stderr, "gen-cert: %v\n", err)
			return 1
		}
	}
	fmt.Printf("certificate  %s\nprivate key  %s\nthumbprint   %s (SHA-1, as servers show it)\napplication  %s\n", certPath, keyPath, res.Thumbprint, *uri)
	fmt.Println("Set source.certificate_file and source.private_key_file, then trust the certificate on the server.")
	return 0
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
