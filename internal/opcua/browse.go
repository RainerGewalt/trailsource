package opcua

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"

	"github.com/RainerGewalt/trailsource/internal/inventory"
)

// VariableNode is a browsed Variable node with the attributes the adapter needs.
type VariableNode struct {
	NodeID *ua.NodeID
	Item   inventory.Item
}

// BrowseResult is the outcome of one browse.
type BrowseResult struct {
	Nodes []VariableNode
	// Truncated is true when max_nodes stopped the walk. The inventory is
	// then a deterministic prefix of the address space, not all of it.
	Truncated bool
	// Visited counts every node read, objects and variables.
	Visited int
}

// BrowseVariables walks the address space from each root along HasComponent,
// Organizes and HasProperty references and returns all Variable nodes sorted
// by browse path. The browse path is the dot-joined chain of BrowseNames
// below the root (namespace prefixes stripped).
//
// A browse never produces a partial inventory by accident: an unreadable root
// or any transport or session error aborts it with an error, so a dying
// connection cannot be mistaken for an address space that lost its nodes.
// Errors that concern a single node (unknown node, access denied) skip that
// node only.
func BrowseVariables(ctx context.Context, client *opcua.Client, roots []*ua.NodeID, maxDepth, maxNodes int, namespaces []string) (BrowseResult, error) {
	if maxDepth <= 0 {
		maxDepth = 10
	}
	if maxNodes <= 0 {
		maxNodes = 1000
	}
	w := &walker{client: client, maxDepth: maxDepth, maxNodes: maxNodes, visited: map[string]bool{}, namespaces: namespaces}
	for _, root := range roots {
		if err := w.node(ctx, client.Node(root), "", 0); err != nil {
			return BrowseResult{}, err
		}
	}
	out := w.out
	sort.Slice(out, func(i, j int) bool {
		if out[i].Item.BrowsePath == out[j].Item.BrowsePath {
			return out[i].Item.NodeID < out[j].Item.NodeID
		}
		return out[i].Item.BrowsePath < out[j].Item.BrowsePath
	})
	return BrowseResult{Nodes: out, Truncated: w.truncated, Visited: len(w.visited)}, nil
}

type walker struct {
	client     *opcua.Client
	maxDepth   int
	maxNodes   int
	visited    map[string]bool
	namespaces []string
	out        []VariableNode
	truncated  bool
}

func (w *walker) full() bool {
	if len(w.out) >= w.maxNodes {
		w.truncated = true
		return true
	}
	return false
}

func (w *walker) node(ctx context.Context, node *opcua.Node, parentPath string, level int) error {
	if level > w.maxDepth || w.full() {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	key := node.ID.String()
	if w.visited[key] {
		return nil
	}
	w.visited[key] = true

	def, nodeClass, err := readDefinition(ctx, node, parentPath, w.namespaces)
	if err != nil {
		if level == 0 {
			return fmt.Errorf("browse root %s unreadable: %w", key, err)
		}
		if IsConnectionError(err) {
			return fmt.Errorf("read %s: %w", key, err)
		}
		log.Printf("browse: skip unreadable node=%s err=%v", key, err)
		return nil
	}
	if nodeClass == ua.NodeClassVariable {
		if w.full() {
			return nil
		}
		w.out = append(w.out, def)
	}
	for _, refType := range []uint32{id.HasComponent, id.Organizes, id.HasProperty} {
		children, err := node.ReferencedNodes(ctx, refType, ua.BrowseDirectionForward, ua.NodeClassAll, true)
		if err != nil {
			if IsConnectionError(err) {
				return fmt.Errorf("browse %s: %w", key, err)
			}
			log.Printf("browse: children failed node=%s ref=%d err=%v", key, refType, err)
			continue
		}
		for _, child := range children {
			if err := w.node(ctx, child, def.Item.BrowsePath, level+1); err != nil {
				return err
			}
			if w.truncated {
				return nil
			}
		}
	}
	return nil
}

// nodeLevelStatus lists service results that concern one node only. Every
// other error during a browse (transport, secure channel, session, timeout)
// means the browse as a whole cannot be trusted.
var nodeLevelStatus = map[ua.StatusCode]bool{
	ua.StatusBadNodeIDUnknown:            true,
	ua.StatusBadNodeIDInvalid:            true,
	ua.StatusBadAttributeIDInvalid:       true,
	ua.StatusBadUserAccessDenied:         true,
	ua.StatusBadNotReadable:              true,
	ua.StatusBadNotSupported:             true,
	ua.StatusBadReferenceTypeIDInvalid:   true,
	ua.StatusBadBrowseDirectionInvalid:   true,
	ua.StatusBadNoContinuationPoints:     true,
	ua.StatusBadContinuationPointInvalid: true,
	ua.StatusBadViewIDUnknown:            true,
	ua.StatusBadNodeClassInvalid:         true,
	ua.StatusBadIndexRangeInvalid:        true,
	ua.StatusBadDataEncodingInvalid:      true,
	ua.StatusBadDataEncodingUnsupported:  true,
	ua.StatusBadTooManyOperations:        true,
	ua.StatusBadEncodingLimitsExceeded:   true,
	ua.StatusBadResponseTooLarge:         true,
	ua.StatusBadDecodingError:            true,
}

// IsConnectionError reports whether err means the session or transport is no
// longer usable, as opposed to a problem with one node.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}
	var code ua.StatusCode
	if errors.As(err, &code) {
		return !nodeLevelStatus[code]
	}
	return true
}

func readDefinition(ctx context.Context, node *opcua.Node, parentPath string, namespaces []string) (VariableNode, ua.NodeClass, error) {
	attrs, err := node.Attributes(ctx,
		ua.AttributeIDNodeClass,
		ua.AttributeIDBrowseName,
		ua.AttributeIDDescription,
		ua.AttributeIDAccessLevel,
		ua.AttributeIDUserAccessLevel,
		ua.AttributeIDDataType,
	)
	if err != nil {
		return VariableNode{}, 0, err
	}
	if len(attrs) < 6 {
		return VariableNode{}, 0, fmt.Errorf("expected 6 attributes, got %d", len(attrs))
	}
	item := inventory.Item{NodeID: node.ID.String(), NamespaceIndex: node.ID.Namespace()}
	if int(item.NamespaceIndex) < len(namespaces) {
		item.NamespaceURI = namespaces[item.NamespaceIndex]
	}

	var nodeClass ua.NodeClass
	if ok(attrs[0]) {
		if v, isInt := variantUint(attrs[0].Value); isInt {
			nodeClass = ua.NodeClass(v)
		}
	}
	item.NodeClass = nodeClass.String()

	item.BrowseName = node.ID.String()
	if ok(attrs[1]) {
		if qn, isQN := attrs[1].Value.Value().(*ua.QualifiedName); isQN && qn != nil {
			item.BrowseName = qn.Name
		} else {
			item.BrowseName = stripNamespacePrefix(attrs[1].Value.String())
		}
	}
	if ok(attrs[2]) {
		if lt, isLT := attrs[2].Value.Value().(*ua.LocalizedText); isLT && lt != nil {
			item.Description = strings.TrimSpace(lt.Text)
		} else {
			item.Description = strings.TrimSpace(attrs[2].Value.String())
		}
	}
	if ok(attrs[3]) {
		if v, isInt := variantUint(attrs[3].Value); isInt {
			item.AccessLevel = uint8(v)
			item.Writable = ua.AccessLevelType(v)&ua.AccessLevelTypeCurrentWrite != 0
		}
	}
	if ok(attrs[4]) {
		if v, isInt := variantUint(attrs[4].Value); isInt {
			item.UserAccessLevel = uint8(v)
			item.UserWritable = ua.AccessLevelType(v)&ua.AccessLevelTypeCurrentWrite != 0
		}
	}
	if ok(attrs[5]) {
		if nid, isNID := attrs[5].Value.Value().(*ua.NodeID); isNID {
			item.DataType = DataTypeName(nid)
		}
	}
	item.BrowsePath = joinPath(parentPath, item.BrowseName)
	return VariableNode{NodeID: node.ID, Item: item}, nodeClass, nil
}

func ok(dv *ua.DataValue) bool {
	return dv != nil && dv.Status == ua.StatusOK && dv.Value != nil
}

func stripNamespacePrefix(name string) string {
	if i := strings.Index(name, ":"); i > 0 && i < 4 {
		allDigits := true
		for _, c := range name[:i] {
			if c < '0' || c > '9' {
				allDigits = false
			}
		}
		if allDigits {
			return name[i+1:]
		}
	}
	return name
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	if name == "" {
		return parent
	}
	return parent + "." + name
}

func variantUint(v *ua.Variant) (uint32, bool) {
	if v == nil {
		return 0, false
	}
	switch value := v.Value().(type) {
	case uint32:
		return value, true
	case int32:
		return uint32(value), true
	case uint16:
		return uint32(value), true
	case int16:
		return uint32(value), true
	case uint8:
		return uint32(value), true
	case int8:
		return uint32(value), true
	case int:
		return uint32(value), true
	case uint:
		return uint32(value), true
	default:
		return 0, false
	}
}

// DataTypeName maps the builtin OPC UA data type NodeIds to short names and
// leaves every other type as its NodeId string.
func DataTypeName(nodeID *ua.NodeID) string {
	if nodeID == nil {
		return ""
	}
	if nodeID.Namespace() != 0 {
		return nodeID.String()
	}
	switch nodeID.IntID() {
	case id.Boolean:
		return "bool"
	case id.SByte:
		return "int8"
	case id.Byte:
		return "uint8"
	case id.Int16:
		return "int16"
	case id.UInt16:
		return "uint16"
	case id.Int32:
		return "int32"
	case id.UInt32:
		return "uint32"
	case id.Int64:
		return "int64"
	case id.UInt64:
		return "uint64"
	case id.Float:
		return "float32"
	case id.Double:
		return "float64"
	case id.String:
		return "string"
	case id.DateTime, id.UtcTime:
		return "datetime"
	case id.ByteString:
		return "bytestring"
	case id.LocalizedText:
		return "localizedtext"
	case id.NodeID:
		return "nodeid"
	default:
		return nodeID.String()
	}
}
