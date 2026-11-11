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
	"slices"
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
		PasswordFile           string   `yaml:"password_file"` // file with the password, mode 0600; instead of password
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
		BrowseTimeoutS         int      `yaml:"browse_timeout_s"`     // whole browse; large address spaces need more
		LivenessIntervalS      int      `yaml:"liveness_interval_s"`  // read ServerStatus.CurrentTime this often; detects half-open connections
		KeepAliveIntervalS     int      `yaml:"keepalive_interval_s"` // requested keep-alive period of the subscription; its absence (x1.5) is a stall
		ApplicationURI         string   `yaml:"application_uri"`      // without certificate_file; with one, the certificate's URI is used
		ServerThumbprint       string   `yaml:"server_thumbprint"`    // SHA-1 of the server certificate; when set, any other certificate is refused
		PKIDir                 string   `yaml:"pki_dir"`              // trust list and own certificate (OPC UA layout); default "pki"
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

// ExpandEnv replaces ${NAME} with the environment value in a text. Unset
// variables expand to the empty string; their names are returned, each
// once, so callers can warn. Load does not expand the text of a
// configuration file this way: it replaces placeholders inside the parsed
// YAML values (expandNode), so a value with quotes, backslashes or comment
// characters cannot change the document.
func ExpandEnv(text string) (string, []string) {
	var missing []string
	out := envPattern.ReplaceAllStringFunc(text, func(m string) string {
		return lookupEnv(m[2:len(m)-1], &missing)
	})
	return out, missing
}

// lookupEnv returns the value of name and records it in missing, once,
// when it is not set.
func lookupEnv(name string, missing *[]string) string {
	v, ok := os.LookupEnv(name)
	if !ok && !slices.Contains(*missing, name) {
		*missing = append(*missing, name)
	}
	return v
}

// expandNode replaces ${NAME} placeholders in every scalar of a parsed YAML
// document with the variable's value as it is. A plain (unquoted) scalar is
// then typed again from its new value, as if the value had been written
// there (rebrowse_interval_s: ${N} is a number), except that a non-empty
// value that would read as null stays a string; a quoted scalar stays a
// string and an explicitly tagged one keeps its tag.
func expandNode(n *yaml.Node, missing *[]string) {
	if n == nil {
		return
	}
	if n.Kind == yaml.ScalarNode && envPattern.MatchString(n.Value) {
		n.Value = envPattern.ReplaceAllStringFunc(n.Value, func(m string) string {
			return lookupEnv(m[2:len(m)-1], missing)
		})
		const explicit = yaml.TaggedStyle | yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle | yaml.LiteralStyle | yaml.FoldedStyle
		if n.Style&explicit == 0 {
			n.Tag = ""
			if n.Value != "" && n.ShortTag() == "!!null" {
				n.Tag = "!!str"
			}
		}
	}
	for _, c := range n.Content {
		expandNode(c, missing)
	}
}

// Load reads the file, replaces ${NAME} placeholders in its values with
// environment variables, validates it and computes Version. The names of
// unset variables are returned even when loading fails.
func Load(path string) (*Config, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		_, missing := ExpandEnv(string(raw))
		return nil, missing, fmt.Errorf("%s: parse yaml: %w", path, err)
	}
	var missing []string
	expandNode(&doc, &missing)
	cfg, err := parseNode(&doc)
	if err != nil {
		return nil, missing, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Path = path
	if cfg.Source.PasswordFile != "" {
		pw, err := ReadSecretFile(cfg.Source.PasswordFile)
		if err != nil {
			return nil, missing, fmt.Errorf("%s: source.password_file: %w", path, err)
		}
		cfg.Source.Password = pw
	}
	return cfg, missing, nil
}

// ReadSecretFile reads a secret from a file that only its owner may read
// (mode 0600 or stricter). A trailing newline is removed. The secret never
// enters config_version: only the file's path is part of the configuration.
func ReadSecretFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by group or others (mode %04o); chmod 600 it", path, st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

// Parse parses YAML bytes without placeholders, applies defaults, validates
// and computes Version.
func Parse(text []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	return parseNode(&doc)
}

// parseNode decodes a parsed document, applies defaults, validates and
// computes Version over the same document.
func parseNode(doc *yaml.Node) (*Config, error) {
	var cfg Config
	var generic any
	if doc.Kind != 0 { // an empty file is an empty configuration
		if err := doc.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
		if err := doc.Decode(&generic); err != nil {
			return nil, fmt.Errorf("parse yaml for hashing: %w", err)
		}
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	version, err := versionOfGeneric(generic, DefaultSecretKeys)
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
	return versionOfGeneric(generic, secretKeys)
}

func versionOfGeneric(generic any, secretKeys map[string]bool) (string, error) {
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
	if c.Source.KeepAliveIntervalS <= 0 {
		c.Source.KeepAliveIntervalS = 10
	}
	if c.Source.ApplicationURI == "" {
		c.Source.ApplicationURI = "urn:trailsource:client"
	}
	if c.Source.PKIDir == "" {
		c.Source.PKIDir = "pki"
	}
	if c.Source.SecurityMode != "None" {
		// trailsource gen-cert --pki writes the client certificate here.
		if c.Source.CertificateFile == "" {
			c.Source.CertificateFile = filepath.Join(c.Source.PKIDir, "own", "certs", "client.der")
		}
		if c.Source.PrivateKeyFile == "" {
			c.Source.PrivateKeyFile = filepath.Join(c.Source.PKIDir, "own", "private", "client.key.pem")
		}
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
		errs = append(errs, unknownValue("source.auth", c.Source.Auth, Logins))
	}
	// Security settings are compared exactly: an unknown value must never
	// fall back to an unsecured session.
	policyOK, modeOK := slices.Contains(SecurityPolicies, c.Source.SecurityPolicy), slices.Contains(SecurityModes, c.Source.SecurityMode)
	if !policyOK {
		errs = append(errs, unknownValue("source.security_policy", c.Source.SecurityPolicy, SecurityPolicies))
	}
	if !modeOK {
		errs = append(errs, unknownValue("source.security_mode", c.Source.SecurityMode, SecurityModes))
	}
	if policyOK && modeOK {
		switch {
		case c.Source.SecurityPolicy == "None" && c.Source.SecurityMode != "None":
			errs = append(errs, fmt.Errorf("source.security_mode %s needs a security_policy other than None", c.Source.SecurityMode))
		case c.Source.SecurityPolicy != "None" && c.Source.SecurityMode == "None":
			errs = append(errs, fmt.Errorf("source.security_policy %s needs security_mode Sign or SignAndEncrypt; with None the channel is not secured", c.Source.SecurityPolicy))
		}
	}
	if c.MQTT.QoS < 0 || c.MQTT.QoS > 2 {
		errs = append(errs, fmt.Errorf("mqtt.qos must be 0, 1 or 2, got %d", c.MQTT.QoS))
	}
	if strings.ContainsAny(c.MQTT.TopicPrefix, "+#") {
		errs = append(errs, errors.New("mqtt.topic_prefix must not contain wildcards"))
	}
	return errors.Join(errs...)
}

// The values the adapter accepts, spelt as OPC UA names them.
var (
	SecurityPolicies = []string{"None", "Basic128Rsa15", "Basic256", "Basic256Sha256", "Aes128_Sha256_RsaOaep", "Aes256_Sha256_RsaPss"}
	SecurityModes    = []string{"None", "Sign", "SignAndEncrypt"}
	Logins           = []string{"anonymous", "username"}
)

// unknownValue names the setting, the value and the accepted values, and
// suggests the accepted value that differs only in case, "_", "-" or
// spaces.
func unknownValue(setting, got string, allowed []string) error {
	fold := func(s string) string {
		return strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(s))
	}
	for _, a := range allowed {
		if fold(a) == fold(got) {
			return fmt.Errorf("%s %q is not supported; did you mean %q? (one of %s)", setting, got, a, strings.Join(allowed, ", "))
		}
	}
	return fmt.Errorf("%s %q is not supported; use one of %s", setting, got, strings.Join(allowed, ", "))
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

// KeepAliveCount is the MaxKeepAliveCount requested for the subscription:
// keepalive_interval_s expressed in publishing intervals (at least 1).
func (c *Config) KeepAliveCount() uint32 {
	n := (time.Duration(c.Source.KeepAliveIntervalS)*time.Second + c.SubscriptionInterval() - 1) / c.SubscriptionInterval()
	if n < 1 {
		n = 1
	}
	return uint32(n)
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
