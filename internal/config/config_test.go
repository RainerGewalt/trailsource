package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

const sample = `
site: plant-a
line: ext-01
device: extruder-01
source:
  endpoint: opc.tcp://opcua-sim:4840/trailsource
  auth: username
  username: reader
  password: hunter2
mqtt:
  broker: tcp://mosquitto:1883
  password: broker-secret
journal:
  enabled: true
`

func TestParseDefaultsAndVersion(t *testing.T) {
	cfg, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source.ID != "plant-a-ext-01-extruder-01-opcua" {
		t.Fatalf("source id %q", cfg.Source.ID)
	}
	if cfg.MQTT.TopicPrefix != "trailsource/plant-a/ext-01/extruder-01" {
		t.Fatalf("topic prefix %q", cfg.MQTT.TopicPrefix)
	}
	if cfg.Journal.Path != "journal/plant-a-ext-01-extruder-01-opcua.jsonl" {
		t.Fatalf("journal path %q", cfg.Journal.Path)
	}
	if !strings.HasPrefix(cfg.Version, "sha256:") || len(cfg.Version) != 71 {
		t.Fatalf("config version %q", cfg.Version)
	}
}

func TestVersionMasksSecretsButTracksSettings(t *testing.T) {
	a, _ := Parse([]byte(sample))
	b, _ := Parse([]byte(strings.ReplaceAll(strings.ReplaceAll(sample, "hunter2", "rotated"), "broker-secret", "other")))
	if a.Version != b.Version {
		t.Fatal("rotating a secret must not change config_version")
	}
	c, _ := Parse([]byte(strings.ReplaceAll(sample, "opcua-sim:4840", "plc-07:4840")))
	if a.Version == c.Version {
		t.Fatal("changing the endpoint must change config_version")
	}
	d, _ := Parse([]byte(strings.ReplaceAll(sample, "username: reader", "username: writer")))
	if a.Version != d.Version {
		t.Fatal("username is masked and must not change config_version")
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("TS_TEST_PW", "from-env")
	out, missing := ExpandEnv("password: ${TS_TEST_PW}\nx: ${TS_TEST_UNSET}")
	if out != "password: from-env\nx: " || len(missing) != 1 || missing[0] != "TS_TEST_UNSET" {
		t.Fatalf("got %q missing %v", out, missing)
	}
}

func TestValidate(t *testing.T) {
	if _, err := Parse([]byte("site: a\nline: b\ndevice: c\nsource:\n  endpoint: http://x\n")); err == nil {
		t.Fatal("non opc.tcp endpoint accepted")
	}
	if _, err := Parse([]byte("site: a\nsource:\n  endpoint: opc.tcp://x\n")); err == nil {
		t.Fatal("missing line/device accepted")
	}
}

func TestSamplingIntervalDefaultsToSubscriptionInterval(t *testing.T) {
	base := "site: s\nline: l\ndevice: d\nsource:\n  endpoint: opc.tcp://h:4840\n  subscription_interval_ms: 750\n"
	cfg, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SamplingInterval() != 750*time.Millisecond {
		t.Fatalf("default sampling interval %s", cfg.SamplingInterval())
	}
	cfg, err = Parse([]byte(base + "  sampling_interval_ms: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SamplingInterval() != 0 {
		t.Fatalf("explicit 0 (server's fastest rate) must be kept, got %s", cfg.SamplingInterval())
	}
}

// Credentials never reach config_version: the hashed document has them
// masked, so neither the hash input nor the hash depends on them.
func TestCredentialsAreMaskedInConfigVersion(t *testing.T) {
	var generic any
	if err := yaml.Unmarshal([]byte(sample), &generic); err != nil {
		t.Fatal(err)
	}
	masked, err := evidence.CanonicalJCS(Mask(normalize(generic), DefaultSecretKeys))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"reader", "hunter2", "broker-secret"} {
		if strings.Contains(string(masked), secret) {
			t.Fatalf("%q reaches the config_version input: %s", secret, masked)
		}
	}
	a, _ := Parse([]byte(sample))
	b, _ := Parse([]byte(strings.NewReplacer("reader", "other-user", "hunter2", "other-password").Replace(sample)))
	if a.Version != b.Version {
		t.Fatal("changing credentials changed config_version")
	}
}

func TestPasswordFileMustBeOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "opcua.password")
	if err := os.WriteFile(pw, []byte("s3cret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "c.yaml")
	text := strings.Replace(sample, "  password: hunter2\n", "  password_file: "+pw+"\n", 1)
	if err := os.WriteFile(cfgPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(cfgPath); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("a group-readable password file must be refused, got %v", err)
	}
	if err := os.Chmod(pw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source.Password != "s3cret" {
		t.Fatalf("password not read: %q", cfg.Source.Password)
	}
	before := cfg.Version
	if err := os.WriteFile(pw, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = Load(cfgPath)
	if err != nil || cfg.Version != before || cfg.Source.Password != "rotated" {
		t.Fatalf("rotating the password must not change config_version: %v", err)
	}
}
