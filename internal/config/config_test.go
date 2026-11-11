package config

import (
	"os"
	"path/filepath"
	"strconv"
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

// minimal is the smallest configuration that validates; tests append to it.
const minimal = "site: s\nline: l\ndevice: d\n"

func withSource(lines ...string) string {
	return minimal + "source:\n  endpoint: opc.tcp://h:4840\n" + strings.Join(lines, "\n") + "\n"
}

// A security setting that is not spelled exactly as OPC UA names it must
// stop the configuration: before 0.1.2 an unknown security_mode silently
// opened an unencrypted session, and the records still named the
// configured mode.
func TestSecuritySettingsAreExact(t *testing.T) {
	for _, tc := range []struct {
		policy, mode string
		want         []string // all in the error
	}{
		{"None", "signandencrypt", []string{`source.security_mode "signandencrypt"`, `did you mean "SignAndEncrypt"`}},
		{"Basic256Sha256", "SignAndEncrpyt", []string{`source.security_mode "SignAndEncrpyt"`, "None, Sign, SignAndEncrypt"}},
		{"basic256sha256", "SignAndEncrypt", []string{`source.security_policy "basic256sha256"`, `did you mean "Basic256Sha256"`}},
		{"Aes128Sha256RsaOaep", "Sign", []string{`did you mean "Aes128_Sha256_RsaOaep"`}},
		{"Basic256Sha256", "None", []string{"security_policy Basic256Sha256 needs security_mode Sign or SignAndEncrypt"}},
		{"None", "SignAndEncrypt", []string{"security_mode SignAndEncrypt needs a security_policy other than None"}},
	} {
		_, err := Parse([]byte(withSource("  security_policy: "+tc.policy, "  security_mode: "+tc.mode)))
		if err == nil {
			t.Errorf("%s/%s accepted", tc.policy, tc.mode)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s/%s: error %q lacks %q", tc.policy, tc.mode, err, w)
			}
		}
	}
	for _, ok := range [][2]string{{"None", "None"}, {"Basic256Sha256", "Sign"}, {"Basic256Sha256", "SignAndEncrypt"},
		{"Aes128_Sha256_RsaOaep", "SignAndEncrypt"}, {"Aes256_Sha256_RsaPss", "Sign"}, {"Basic128Rsa15", "Sign"}, {"Basic256", "SignAndEncrypt"}} {
		if _, err := Parse([]byte(withSource("  security_policy: "+ok[0], "  security_mode: "+ok[1]))); err != nil {
			t.Errorf("%s/%s refused: %v", ok[0], ok[1], err)
		}
	}
	if _, err := Parse([]byte(withSource())); err != nil {
		t.Errorf("the default None/None refused: %v", err)
	}
}

func TestAuthIsExact(t *testing.T) {
	_, err := Parse([]byte(withSource("  auth: Anonymous")))
	if err == nil || !strings.Contains(err.Error(), `did you mean "anonymous"`) {
		t.Fatalf("auth Anonymous: want an error with the correct spelling, got %v", err)
	}
	_, err = Parse([]byte(withSource("  auth: certificate")))
	if err == nil || !strings.Contains(err.Error(), "anonymous, username") {
		t.Fatalf("auth certificate: want an error that lists the supported logins, got %v", err)
	}
}

func TestQoSOutOfRangeIsAnError(t *testing.T) {
	for _, q := range []string{"3", "-1", "7"} {
		if _, err := Parse([]byte(minimal + "source:\n  endpoint: opc.tcp://h:4840\nmqtt:\n  qos: " + q + "\n")); err == nil || !strings.Contains(err.Error(), "mqtt.qos must be 0, 1 or 2") {
			t.Errorf("qos %s: want an error, got %v", q, err)
		}
	}
	for _, q := range []string{"0", "1", "2"} {
		cfg, err := Parse([]byte(minimal + "source:\n  endpoint: opc.tcp://h:4840\nmqtt:\n  qos: " + q + "\n"))
		if err != nil || strconv.Itoa(cfg.MQTT.QoS) != q {
			t.Errorf("qos %s: %v %+v", q, err, cfg)
		}
	}
}

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A ${VAR} placeholder is replaced by the variable's value as it is: YAML
// quoting, escapes and comment characters in the value do not change it,
// and do not break the file.
func TestEnvValuesArriveUnchanged(t *testing.T) {
	for _, pw := range []string{`tab\there`, `quo"te`, `C:\path\to`, `it's`, `a: b # c`, "line\nbreak", `null`, `~`, `123`, `true`, ` padded `} {
		for _, form := range []string{`"${TS_TEST_PW}"`, `'${TS_TEST_PW}'`, `${TS_TEST_PW}`} {
			t.Setenv("TS_TEST_PW", pw)
			cfg, missing, err := Load(writeConfig(t, withSource("  auth: username", "  username: reader", "  password: "+form)))
			if err != nil {
				t.Errorf("password %q as %s: %v", pw, form, err)
				continue
			}
			if cfg.Source.Password != pw || len(missing) != 0 {
				t.Errorf("password %q as %s arrived as %q (missing %v)", pw, form, cfg.Source.Password, missing)
			}
		}
	}
}

// Numbers and booleans from placeholders keep their type, and the
// config_version of a configuration with plain values is the one the
// textual expansion of 0.1.1 produced, so an upgrade does not look like a
// configuration change.
func TestEnvPlaceholdersKeepTypesAndConfigVersion(t *testing.T) {
	t.Setenv("TS_TEST_PORT", "48400")
	t.Setenv("TS_TEST_REBROWSE", "5")
	t.Setenv("TS_TEST_NODES", "true")
	t.Setenv("TS_TEST_PW", "hunter2")
	text := minimal + "source:\n  endpoint: opc.tcp://127.0.0.1:${TS_TEST_PORT}/x\n  rebrowse_interval_s: ${TS_TEST_REBROWSE}\n" +
		"  auth: username\n  username: \"reader\"\n  password: \"${TS_TEST_PW}\"\nmqtt:\n  publish_node_topics: ${TS_TEST_NODES}\n"
	cfg, missing, err := Load(writeConfig(t, text))
	if err != nil || len(missing) != 0 {
		t.Fatal(err, missing)
	}
	if cfg.Source.Endpoint != "opc.tcp://127.0.0.1:48400/x" || cfg.Source.RebrowseIntervalS != 5 || !cfg.MQTT.PublishNodeTopics || cfg.Source.Password != "hunter2" {
		t.Fatalf("placeholders not applied with their types: %+v", cfg.Source)
	}
	expanded, _ := ExpandEnv(text)
	want, err := VersionOf([]byte(expanded), DefaultSecretKeys)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != want {
		t.Fatalf("config_version %s, the textual expansion gives %s", cfg.Version, want)
	}
}

// Unset variables are reported by name, also when the file does not parse,
// and also when the same variable is used twice only once.
func TestMissingVariablesAreReportedEvenOnAParseError(t *testing.T) {
	_, missing, err := Load(writeConfig(t, withSource()+"mqtt:\n  username: ${TS_TEST_UNSET_A}\n  password: ${TS_TEST_UNSET_A}\n"))
	if err != nil || len(missing) != 1 || missing[0] != "TS_TEST_UNSET_A" {
		t.Fatalf("want one missing variable, got %v (%v)", missing, err)
	}
	_, missing, err = Load(writeConfig(t, minimal+"source:\n  endpoint: opc.tcp://h:${TS_TEST_UNSET_B}/x\n   bad: indentation\n"))
	if err == nil {
		t.Fatal("a file that does not parse was accepted")
	}
	if len(missing) != 1 || missing[0] != "TS_TEST_UNSET_B" {
		t.Fatalf("missing variables not reported with the parse error: %v", missing)
	}
}
