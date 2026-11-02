// Package mapping assigns semantic roles, units and scaling to browsed nodes
// from a versioned mapping file. Nodes without a rule get a derived role from
// their browse name so every observation carries a semantic_role.
package mapping

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

// Rule maps a browse path to a semantic role.
type Rule struct {
	Path          string `yaml:"path" json:"path,omitempty"`       // exact browse path
	Pattern       string `yaml:"pattern" json:"pattern,omitempty"` // glob; '*' matches one segment, '**' is not supported
	SemanticRole  string `yaml:"semantic_role" json:"semantic_role"`
	Unit          string `yaml:"unit" json:"unit,omitempty"` // UN/CEFACT Rec. 20 code where possible
	ScaleDecimals *int   `yaml:"scale_decimals" json:"scale_decimals,omitempty"`
	Context       bool   `yaml:"context" json:"context,omitempty"` // repeat as context variable (batch, order, recipe)
}

// File is the mapping document.
type File struct {
	Version     string `yaml:"version" json:"version"`
	Description string `yaml:"description" json:"description,omitempty"`
	Rules       []Rule `yaml:"rules" json:"rules"`

	Path string `yaml:"-" json:"-"`
	Hash string `yaml:"-" json:"hash"` // sha256 over the canonical document
}

// Resolution is the mapping outcome for one node.
type Resolution struct {
	SemanticRole  string
	Source        string // "mapping" or "derived"
	Unit          string
	ScaleDecimals *int
	Context       bool
}

// Load reads and hashes a mapping file.
func Load(filePath string) (*File, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse mapping %s: %w", filePath, err)
	}
	if f.Version == "" {
		f.Version = "0"
	}
	for i, r := range f.Rules {
		if r.SemanticRole == "" {
			return nil, fmt.Errorf("mapping %s: rule %d has no semantic_role", filePath, i)
		}
		if r.Path == "" && r.Pattern == "" {
			return nil, fmt.Errorf("mapping %s: rule %d needs path or pattern", filePath, i)
		}
	}
	var generic any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	h, err := evidence.Hash(toStringKeys(generic))
	if err != nil {
		return nil, err
	}
	f.Path = filePath
	f.Hash = h
	return &f, nil
}

// Empty returns a mapping without rules (every role is derived).
func Empty() *File {
	return &File{Version: "0", Hash: evidence.GenesisHash}
}

// Resolve returns the role of a node. Exact paths win over patterns; the first
// matching pattern in file order wins.
func (f *File) Resolve(browsePath, browseName string) Resolution {
	for _, r := range f.Rules {
		if r.Path != "" && r.Path == browsePath {
			return fromRule(r)
		}
	}
	slashPath := strings.ReplaceAll(browsePath, ".", "/")
	for _, r := range f.Rules {
		if r.Pattern == "" {
			continue
		}
		if ok, _ := path.Match(strings.ReplaceAll(r.Pattern, ".", "/"), slashPath); ok {
			return fromRule(r)
		}
	}
	return Resolution{SemanticRole: DerivedRole(browsePath, browseName), Source: "derived"}
}

func fromRule(r Rule) Resolution {
	return Resolution{SemanticRole: r.SemanticRole, Source: "mapping", Unit: r.Unit, ScaleDecimals: r.ScaleDecimals, Context: r.Context}
}

var splitRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)
var camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)
var acronymRe = regexp.MustCompile(`([A-Z])([A-Z][a-z])`)

// DerivedRole turns a browse name such as "MeltPressureBar" into
// "melt_pressure_bar". It falls back to the browse path when the name is empty.
func DerivedRole(browsePath, browseName string) string {
	raw := browseName
	if strings.TrimSpace(raw) == "" {
		raw = browsePath
	}
	raw = camelRe.ReplaceAllString(raw, "${1}_${2}")
	raw = acronymRe.ReplaceAllString(raw, "${1}_${2}")
	parts := splitRe.Split(raw, -1)
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			clean = append(clean, strings.ToLower(p))
		}
	}
	return strings.Join(clean, "_")
}

func toStringKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = toStringKeys(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = toStringKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = toStringKeys(e)
		}
		return out
	default:
		return v
	}
}
