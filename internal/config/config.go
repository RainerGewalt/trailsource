// Package config loads the YAML configuration of the OPC UA adapter and
// computes config_version: SHA-256 over the canonical (JCS) configuration with
// secret-shaped keys masked, so the hash never contains a secret but still
// changes when any non-secret setting changes.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

// DefaultSecretKeys are masked before hashing. The list mirrors the stdlib
// envelope helper this repository descends from (see docs/CLEAN_ROOM.md).
var DefaultSecretKeys = map[string]bool{
	"password": true, "passwd": true, "pwd": true, "token": true, "secret": true,
	"api_key": true, "apikey": true, "username": true, "user": true,
	"connection_string": true, "private_key_path": true, "cert_path": true,
	"identity": true, "client_key": true,
}

// MaskPlaceholder replaces secret values before hashing.
const MaskPlaceholder = "***"

// Config is the adapter configuration.
type Config struct {
	Revision string `yaml:"revision"` // free-form, user-managed; becomes policy.version of kind "config"

	Producer struct {
		ID      string `yaml:"id"`
		Version string `yaml:"version"` // optional override of the build version
	} `yaml:"producer"`

	Site   string `yaml:"site"`
	Line   string `yaml:"line"`
	Device string `yaml:"device"`

	Source struct {
		ID                     string   `yaml:"id"`
		Endpoint               string   `yaml:"endpoint"`
		SecurityPolicy         string   `yaml:"security_policy"` // None, Basic256Sha256, ...
		SecurityMode           string   `yaml:"security_mode"`   // None, Sign, SignAndEncrypt
		Auth                   string   `yaml:"auth"`            // anonymous | username
		Username               string   `yaml:"username"`
		Password               string   `yaml:"password"`
		CertificateFile        string   `yaml:"certificate_file"`
		PrivateKeyFile         string   `yaml:"private_key_file"`
		BrowseRoots            []string `yaml:"browse_roots"`
		MaxDepth               int      `yaml:"max_depth"`
		MaxNodes               int      `yaml:"max_nodes"`
		SubscriptionIntervalMS int      `yaml:"subscription_interval_ms"`
		SamplingIntervalMS     *int     `yaml:"sampling_interval_ms"` // default: subscription_interval_ms; 0 = the server's fastest rate
		SubscriptionBatchSize  int      `yaml:"subscription_batch_size"`
		RebrowseIntervalS      int      `yaml:"rebrowse_interval_s"`
		SessionBackoffS        int      `yaml:"session_backoff_s"`
		RequestTimeoutS        int      `yaml:"request_timeout_s"`
		BrowseTimeoutS         int      `yaml:"browse_timeout_s"`    // whole browse; large address spaces need more
		LivenessIntervalS      int      `yaml:"liveness_interval_s"` // read ServerStatus.CurrentTime this often; detects half-open connections
		ApplicationURI         string   `yaml:"application_uri"`     // without certificate_file; with one, the certificate's URI is used
		ServerThumbprint       string   `yaml:"server_thumbprint"`   // SHA-1 of the server certificate; when set, any other certificate is refused
	} `yaml:"source"`

	MQTT struct {
		Broker            string `yaml:"broker"`
		ClientID          string `yaml:"client_id"`
		Username          string `yaml:"username"`
		Password          string `yaml:"password"`
		QoS               int    `yaml:"qos"`
		TopicPrefix       string `yaml:"topic_prefix"`
		PublishNodeTopics bool   `yaml:"publish_node_topics"`
		HealthIntervalS   int    `yaml:"health_interval_s"`
		TLS               struct {
			Enabled            bool   `yaml:"enabled"`
			CACert             string `yaml:"ca_cert"`
			ClientCert         string `yaml:"client_cert"`
			ClientKey          string `yaml:"client_key"`
			InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
			ServerName         string `yaml:"server_name"`
		} `yaml:"tls"`
	} `yaml:"mqtt"`

	MappingFile string `yaml:"mapping_file"`
	StateDir    string `yaml:"state_dir"`

	Journal struct {
		Enabled bool   `yaml:"enabled"`
		Path    string `yaml:"path"`
	} `yaml:"journal"`

	// Derived, not part of the YAML.
	Version string `yaml:"-"` // config_version: sha256 over masked canonical config
	Path    string `yaml:"-"`
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnv replaces ${NAME} with the environment value. Unset variables
// expand to the empty string; the names are returned so callers can warn.
func ExpandEnv(text string) (string, []string) {
	var missing []string
	out := envPattern.ReplaceAllStringFunc(text, func(m string) string {
		name := m[2 : len(m)-1]
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	return out, missing
}

// Load reads, expands, validates the file and computes Version.
func Load(path string) (*Config, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	text, missing := ExpandEnv(string(raw))
	cfg, err := Parse([]byte(text))
	if err != nil {
		return nil, missing, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Path = path
	return cfg, missing, nil
}

// Parse parses YAML bytes (already env-expanded), applies defaults, validates
// and computes Version.
func Parse(text []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(text, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	version, err := VersionOf(text, DefaultSecretKeys)
	if err != nil {
		return nil, err
	}
	cfg.Version = version
	return &cfg, nil
}

// VersionOf computes config_version over the generic YAML document with
// secret keys masked.
func VersionOf(text []byte, secretKeys map[string]bool) (string, error) {
	var generic any
	if err := yaml.Unmarshal(text, &generic); err != nil {
		return "", fmt.Errorf("parse yaml for hashing: %w", err)
	}
	masked := Mask(normalize(generic), secretKeys)
	return evidence.Hash(masked)
}

// Mask replaces the value of every key whose lowercase name is in secretKeys.
func Mask(v any, secretKeys map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if secretKeys[strings.ToLower(k)] {
				out[k] = MaskPlaceholder
			} else {
				out[k] = Mask(val, secretKeys)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = Mask(e, secretKeys)
		}
		return out
	default:
		return v
	}
}

// normalize converts yaml.v3 generic output to map[string]any / []any trees.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalize(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalize(e)
		}
		return out
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	default:
		return v
	}
}

func (c *Config) applyDefaults() {
	if c.Revision == "" {
		c.Revision = "0"
	}
	if c.Source.SecurityPolicy == "" {
		c.Source.SecurityPolicy = "None"
	}
	if c.Source.SecurityMode == "" {
		c.Source.SecurityMode = "None"
	}
	if c.Source.Auth == "" {
		c.Source.Auth = "anonymous"
	}
	if len(c.Source.BrowseRoots) == 0 {
		c.Source.BrowseRoots = []string{"ns=0;i=85"} // Objects folder
	}
	if c.Source.MaxDepth <= 0 {
		c.Source.MaxDepth = 16
	}
	if c.Source.MaxNodes <= 0 {
		c.Source.MaxNodes = 2500
	}
	if c.Source.SubscriptionIntervalMS <= 0 {
		c.Source.SubscriptionIntervalMS = 500
	}
	if c.Source.SamplingIntervalMS == nil || *c.Source.SamplingIntervalMS < 0 {
		v := c.Source.SubscriptionIntervalMS
		c.Source.SamplingIntervalMS = &v
	}
	if c.Source.SubscriptionBatchSize <= 0 {
		c.Source.SubscriptionBatchSize = 50
	}
	if c.Source.RebrowseIntervalS <= 0 {
		c.Source.RebrowseIntervalS = 30
	}
	if c.Source.SessionBackoffS <= 0 {
		c.Source.SessionBackoffS = 5
	}
	if c.Source.RequestTimeoutS <= 0 {
		c.Source.RequestTimeoutS = 12
	}
	if c.Source.BrowseTimeoutS <= 0 {
		c.Source.BrowseTimeoutS = 120
	}
	if c.Source.LivenessIntervalS <= 0 {
		c.Source.LivenessIntervalS = 10
	}
	if c.Source.ApplicationURI == "" {
		c.Source.ApplicationURI = "urn:trailsource:client"
	}
	if c.Source.ID == "" {
		c.Source.ID = strings.Trim(strings.Join([]string{tok(c.Site), tok(c.Line), tok(c.Device), "opcua"}, "-"), "-")
	}
	if c.Producer.ID == "" {
		c.Producer.ID = "trailsource-" + c.Source.ID
	}
	if c.MQTT.Broker == "" {
		c.MQTT.Broker = "tcp://localhost:1883"
	}
	if c.MQTT.ClientID == "" {
		c.MQTT.ClientID = c.Producer.ID
	}
	if c.MQTT.QoS < 0 || c.MQTT.QoS > 2 {
		c.MQTT.QoS = 1
	}
	if c.MQTT.TopicPrefix == "" {
		c.MQTT.TopicPrefix = "trailsource/" + tok(c.Site) + "/" + tok(c.Line) + "/" + tok(c.Device)
	}
	c.MQTT.TopicPrefix = strings.Trim(c.MQTT.TopicPrefix, "/")
	if c.MQTT.HealthIntervalS <= 0 {
		c.MQTT.HealthIntervalS = 15
	}
	if c.StateDir == "" {
		c.StateDir = "state"
	}
	if c.Journal.Enabled && c.Journal.Path == "" {
		c.Journal.Path = filepath.Join("journal", c.Source.ID+".jsonl")
	}
}

func (c *Config) validate() error {
	var errs []error
	if c.Site == "" || c.Line == "" || c.Device == "" {
		errs = append(errs, errors.New("site, line and device are required"))
	}
	if c.Source.Endpoint == "" {
		errs = append(errs, errors.New("source.endpoint is required"))
	}
	if !strings.HasPrefix(c.Source.Endpoint, "opc.tcp://") {
		errs = append(errs, fmt.Errorf("source.endpoint must start with opc.tcp://, got %q", c.Source.Endpoint))
	}
	switch c.Source.Auth {
	case "anonymous":
	case "username":
		if c.Source.Username == "" {
			errs = append(errs, errors.New("source.auth=username requires source.username"))
		}
	default:
		errs = append(errs, fmt.Errorf("source.auth must be anonymous or username, got %q", c.Source.Auth))
	}
	if strings.ContainsAny(c.MQTT.TopicPrefix, "+#") {
		errs = append(errs, errors.New("mqtt.topic_prefix must not contain wildcards"))
	}
	return errors.Join(errs...)
}

func tok(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// Durations derived from the integer settings.
func (c *Config) SubscriptionInterval() time.Duration {
	return time.Duration(c.Source.SubscriptionIntervalMS) * time.Millisecond
}
func (c *Config) SamplingInterval() time.Duration {
	return time.Duration(*c.Source.SamplingIntervalMS) * time.Millisecond
}
func (c *Config) RebrowseInterval() time.Duration {
	return time.Duration(c.Source.RebrowseIntervalS) * time.Second
}
func (c *Config) SessionBackoff() time.Duration {
	return time.Duration(c.Source.SessionBackoffS) * time.Second
}
func (c *Config) RequestTimeout() time.Duration {
	return time.Duration(c.Source.RequestTimeoutS) * time.Second
}
func (c *Config) LivenessInterval() time.Duration {
	return time.Duration(c.Source.LivenessIntervalS) * time.Second
}
func (c *Config) BrowseTimeout() time.Duration {
	return time.Duration(c.Source.BrowseTimeoutS) * time.Second
}
func (c *Config) HealthInterval() time.Duration {
	return time.Duration(c.MQTT.HealthIntervalS) * time.Second
}
