package config

import (
	"strings"
	"testing"
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
