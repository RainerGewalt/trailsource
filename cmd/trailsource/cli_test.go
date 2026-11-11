package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The command line is tested as a user runs it: the test binary re-executes
// itself with TRAILSOURCE_TEST_MAIN=1 and the arguments, and main runs.
func TestMain(m *testing.M) {
	if os.Getenv("TRAILSOURCE_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runCLI(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TRAILSOURCE_TEST_MAIN=1")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

func TestUnknownCommandIsNamed(t *testing.T) {
	_, stderr, code := runCLI(t, t.TempDir(), "frobnicate")
	if code != 2 || !strings.Contains(stderr, `unknown command "frobnicate"`) {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
}

func TestHelpNamesTheEndpointsFlags(t *testing.T) {
	_, stderr, _ := runCLI(t, t.TempDir(), "help")
	for _, want := range []string{"endpoints [--json] [--timeout <d>]", "browse --config <file> [--json] [--mapping-out <file> [--force]]"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("help lacks %q:\n%s", want, stderr)
		}
	}
}

const cliConfig = "site: s\nline: l\ndevice: d\nsource:\n  endpoint: opc.tcp://127.0.0.1:1/unreachable\n  request_timeout_s: 1\n"

// Unset variables are named also when the configuration does not load.
func TestMissingVariablesAreNamedWhenTheConfigFails(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(cfg, []byte(cliConfig+"mqtt:\n  password: ${TS_CLI_UNSET}\n broken: indentation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"run", "browse", "config-version"} {
		_, stderr, code := runCLI(t, dir, cmd, "--config", cfg)
		if code != 1 || !strings.Contains(stderr, "TS_CLI_UNSET") || !strings.Contains(stderr, "parse yaml") {
			t.Errorf("%s: exit %d, want 1 with the parse error and the unset variable; stderr:\n%s", cmd, code, stderr)
		}
	}
}

// A misspelt security mode stops before any connection attempt.
func TestBrowseWithAnUnknownSecurityModeStopsBeforeConnecting(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(cfg, []byte(cliConfig+"  security_mode: signandencrypt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, dir, "browse", "--config", cfg)
	if code != 1 || !strings.Contains(stderr, `did you mean "SignAndEncrypt"`) || strings.Contains(stderr, "connect") {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
}

// browse --mapping-out does not replace an existing mapping file, which
// may hold hand-written roles, units and scaling, unless --force is given;
// it says so before it connects.
func TestMappingOutDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(cfg, []byte(cliConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	mapping := filepath.Join(dir, "mapping.yaml")
	edited := []byte("# edited by hand\nrules: []\n")
	if err := os.WriteFile(mapping, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, dir, "browse", "--config", cfg, "--mapping-out", mapping)
	if code != 1 || !strings.Contains(stderr, "exists") || !strings.Contains(stderr, "--force") {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if got, _ := os.ReadFile(mapping); !bytes.Equal(got, edited) {
		t.Fatalf("mapping file changed:\n%s", got)
	}
	// With --force the check is passed and browse goes on to the server,
	// which is unreachable here.
	_, stderr, code = runCLI(t, dir, "browse", "--config", cfg, "--mapping-out", mapping, "--force")
	if code != 1 || strings.Contains(stderr, "--force") {
		t.Fatalf("with --force: exit %d, stderr:\n%s", code, stderr)
	}
}
