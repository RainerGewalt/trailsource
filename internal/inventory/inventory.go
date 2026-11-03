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
	"regexp"
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

	// NormalizedPath identifies the node's place in the address space
	// independently of namespace indexes: the browse root's NodeId in URI
	// form, then one "/{namespace-uri}BrowseName" segment per level below
	// it. See docs/RECORD_MAPPING.md, "Fingerprint and bindings".
	NormalizedPath string `json:"normalized_path,omitempty"`
	// EntityKey is the stable binding of this variable (see AssignEntityKeys).
	EntityKey string `json:"entity_key,omitempty"`
}

var nsPrefix = regexp.MustCompile(`^ns=(\d+);`)

// NormalizeNodeID replaces the namespace index of a NodeId string with the
// namespace URI ("ns=3;s=X" with URI u becomes "nsu=u;s=X"). Namespace 0 has
// no prefix and stays as it is. Without a URI the string is returned as is.
func NormalizeNodeID(nodeID, namespaceURI string) string {
	if namespaceURI == "" || !nsPrefix.MatchString(nodeID) {
		return nodeID
	}
	return "nsu=" + namespaceURI + ";" + nsPrefix.ReplaceAllString(nodeID, "")
}

// Key is the identity of an item across browses: its NodeId with the
// namespace URI instead of the index. A server that only renumbers its
// namespaces does not change any key.
func (it Item) Key() string {
	return NormalizeNodeID(it.NodeID, it.NamespaceURI)
}

// FingerprintVersion identifies the fingerprint definition. Version 1 hashed
// NodeId|BrowseName|DataType; version 2 hashes NormalizedPath, the NodeId in
// URI form, NodeClass and DataType.
const FingerprintVersion = 2

// Fingerprint returns "sha256:" + hex over the sorted list of
// NormalizedPath|Key|NodeClass|DataType lines (one per item, '\n'
// terminated). Sorting makes it independent of browse and reference order;
// URIs make it independent of namespace indexes; the path makes a rename or
// a move in the tree a change even when NodeIds are reused.
func Fingerprint(items []Item) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		lines = append(lines, it.NormalizedPath+"|"+it.Key()+"|"+it.NodeClass+"|"+it.DataType)
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
	// FingerprintVersion is 0 in snapshots written before version 2.
	FingerprintVersion int    `json:"fingerprint_version,omitempty"`
	TakenAt            string `json:"taken_at"`
	Fingerprint        string `json:"fingerprint"`
	Items              []Item `json:"items"`
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

// Compute returns the diff from old to new, keyed by Key (NodeId with the
// namespace URI), so a renumbered namespace is not a change. Compared are the
// fingerprint-relevant fields (browse name, path, node class, data type) and
// the access levels; a different description alone is not a change. Change
// entries name the NodeId as the new snapshot sees it.
func Compute(oldItems, newItems []Item) Diff {
	oldByID := make(map[string]Item, len(oldItems))
	for _, it := range oldItems {
		oldByID[it.Key()] = it
	}
	newByID := make(map[string]Item, len(newItems))
	for _, it := range newItems {
		newByID[it.Key()] = it
	}
	var d Diff
	for _, it := range newItems {
		prev, ok := oldByID[it.Key()]
		if !ok {
			d.Added = append(d.Added, it)
			continue
		}
		if prev.BrowseName != it.BrowseName {
			d.Changed = append(d.Changed, Change{it.NodeID, "browse_name", prev.BrowseName, it.BrowseName})
		}
		if prev.NodeClass != it.NodeClass {
			d.Changed = append(d.Changed, Change{it.NodeID, "node_class", prev.NodeClass, it.NodeClass})
		}
		if prev.DataType != it.DataType {
			d.Changed = append(d.Changed, Change{it.NodeID, "data_type", prev.DataType, it.DataType})
		}
		switch {
		case prev.BrowsePath != it.BrowsePath:
			d.Changed = append(d.Changed, Change{it.NodeID, "browse_path", prev.BrowsePath, it.BrowsePath})
		case prev.NormalizedPath != "" && it.NormalizedPath != "" && prev.NormalizedPath != it.NormalizedPath:
			// Same display path, different namespace of a BrowseName on the way.
			d.Changed = append(d.Changed, Change{it.NodeID, "normalized_path", prev.NormalizedPath, it.NormalizedPath})
		}
		if prev.AccessLevel != it.AccessLevel {
			d.Changed = append(d.Changed, Change{it.NodeID, "access_level", fmt.Sprint(prev.AccessLevel), fmt.Sprint(it.AccessLevel)})
		}
		if prev.UserAccessLevel != it.UserAccessLevel {
			d.Changed = append(d.Changed, Change{it.NodeID, "user_access_level", fmt.Sprint(prev.UserAccessLevel), fmt.Sprint(it.UserAccessLevel)})
		}
	}
	for _, it := range oldItems {
		if _, ok := newByID[it.Key()]; !ok {
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

// AssignEntityKeys sets EntityKey on every item. The key is EntityKey of the
// browse path, which the walk makes independent of browse and reference
// order. When two items would get the same key (sibling BrowseNames that
// differ only in namespace or in characters Token drops), every member of
// that group gets a suffix derived from its Key, so the result does not
// depend on which of them was found first and no two items share a key.
func AssignEntityKeys(sourceID string, items []Item) {
	groups := map[string][]int{}
	for i := range items {
		base := EntityKey(sourceID, items[i].BrowsePath, items[i].NodeID)
		items[i].EntityKey = base
		groups[base] = append(groups[base], i)
	}
	for base, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		for _, i := range idx {
			sum := sha256.Sum256([]byte(items[i].Key()))
			items[i].EntityKey = base + "~" + hex.EncodeToString(sum[:4])
		}
	}
}

// EntityKey builds the source-scoped key of a node from its browse path:
// lowercase, non-alphanumerics collapsed to '-', prefixed by source id.
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
