// Package inventory models the browsed address space of a source, its
// fingerprint, persisted snapshots and the diff between two snapshots.
package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Item is one variable node of the address space.
type Item struct {
	NodeID          string `json:"node_id"`
	BrowsePath      string `json:"browse_path"`
	BrowseName      string `json:"browse_name"`
	NamespaceIndex  uint16 `json:"namespace_index"`
	NamespaceURI    string `json:"namespace_uri,omitempty"`
	NodeClass       string `json:"node_class"`
	DataType        string `json:"data_type"`
	AccessLevel     uint8  `json:"access_level"`
	UserAccessLevel uint8  `json:"user_access_level"`
	Writable        bool   `json:"writable"`      // AccessLevel has CurrentWrite
	UserWritable    bool   `json:"user_writable"` // UserAccessLevel has CurrentWrite for this session
	Description     string `json:"description,omitempty"`
}

// Fingerprint returns "sha256:" + hex over the sorted list of
// NodeId|BrowseName|DataType lines (one per item, '\n' terminated).
func Fingerprint(items []Item) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		lines = append(lines, it.NodeID+"|"+it.BrowseName+"|"+it.DataType)
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Snapshot is the persisted result of one browse.
type Snapshot struct {
	SourceID    string   `json:"source_id"`
	Endpoint    string   `json:"endpoint"`
	BrowseRoots []string `json:"browse_roots"`
	Generation  uint64   `json:"generation"` // increments on every changed fingerprint
	TakenAt     string   `json:"taken_at"`
	Fingerprint string   `json:"fingerprint"`
	Items       []Item   `json:"items"`
}

// Change describes one attribute change of a node that exists in both snapshots.
type Change struct {
	NodeID string `json:"node_id"`
	Field  string `json:"field"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

// Diff is the difference between two snapshots.
type Diff struct {
	Added   []Item   `json:"added"`
	Removed []Item   `json:"removed"`
	Changed []Change `json:"changed"`
}

// Empty reports whether the diff carries no difference.
func (d Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// Compute returns the diff from old to new, keyed by NodeID. Only the
// fingerprint-relevant fields (browse name, data type) and the access levels
// are compared; a different description alone is not a change.
func Compute(oldItems, newItems []Item) Diff {
	oldByID := make(map[string]Item, len(oldItems))
	for _, it := range oldItems {
		oldByID[it.NodeID] = it
	}
	newByID := make(map[string]Item, len(newItems))
	for _, it := range newItems {
		newByID[it.NodeID] = it
	}
	var d Diff
	for _, it := range newItems {
		prev, ok := oldByID[it.NodeID]
		if !ok {
			d.Added = append(d.Added, it)
			continue
		}
		if prev.BrowseName != it.BrowseName {
			d.Changed = append(d.Changed, Change{it.NodeID, "browse_name", prev.BrowseName, it.BrowseName})
		}
		if prev.DataType != it.DataType {
			d.Changed = append(d.Changed, Change{it.NodeID, "data_type", prev.DataType, it.DataType})
		}
		if prev.BrowsePath != it.BrowsePath {
			d.Changed = append(d.Changed, Change{it.NodeID, "browse_path", prev.BrowsePath, it.BrowsePath})
		}
		if prev.AccessLevel != it.AccessLevel {
			d.Changed = append(d.Changed, Change{it.NodeID, "access_level", fmt.Sprint(prev.AccessLevel), fmt.Sprint(it.AccessLevel)})
		}
		if prev.UserAccessLevel != it.UserAccessLevel {
			d.Changed = append(d.Changed, Change{it.NodeID, "user_access_level", fmt.Sprint(prev.UserAccessLevel), fmt.Sprint(it.UserAccessLevel)})
		}
	}
	for _, it := range oldItems {
		if _, ok := newByID[it.NodeID]; !ok {
			d.Removed = append(d.Removed, it)
		}
	}
	sort.Slice(d.Added, func(i, j int) bool { return d.Added[i].NodeID < d.Added[j].NodeID })
	sort.Slice(d.Removed, func(i, j int) bool { return d.Removed[i].NodeID < d.Removed[j].NodeID })
	sort.Slice(d.Changed, func(i, j int) bool {
		if d.Changed[i].NodeID == d.Changed[j].NodeID {
			return d.Changed[i].Field < d.Changed[j].Field
		}
		return d.Changed[i].NodeID < d.Changed[j].NodeID
	})
	return d
}

// Load reads a snapshot file. found is false when the file does not exist.
func Load(path string) (snap *Snapshot, found bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false, fmt.Errorf("parse inventory snapshot %s: %w", path, err)
	}
	return &s, true, nil
}

// Save writes the snapshot atomically.
func Save(path string, s *Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// EntityKey builds the stable, source-scoped key of a node from its browse
// path: lowercase, non-alphanumerics collapsed to '-', prefixed by source id.
func EntityKey(sourceID, browsePath, nodeID string) string {
	tail := Token(browsePath)
	if tail == "" {
		tail = Token(nodeID)
	}
	if tail == "" {
		tail = "unknown"
	}
	return sourceID + "/" + tail
}

// Token lowercases s and collapses every run of non-alphanumeric characters to
// a single '-'. It is used for entity keys and topic segments.
func Token(s string) string {
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
