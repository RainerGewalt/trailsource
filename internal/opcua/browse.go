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
	// Visited counts every node entered, objects and variables.
	Visited int
	// Requests counts Browse, BrowseNext and Read service calls.
	Requests int
}

// BrowseVariables walks the address space from each root along HasComponent,
// Organizes and HasProperty references and returns all Variable nodes sorted
// by browse path. The browse path is the dot-joined chain of BrowseNames
// below the root (namespace prefixes stripped).
//
// The walk is depth-first. A node at depth d is entered when d <= maxDepth;
// a node reachable on several paths keeps the path it was first entered on;
// the walk stops when maxNodes variables were collected. Requests are
// batched: the children of all siblings are browsed in one Browse call and
// the attributes of all sibling variables are read in one Read call, so the
// number of round trips grows with the number of parents, not of nodes.
//
// A browse never produces a partial inventory by accident: an unreadable root
// or any transport or session error aborts it with an error, so a dying
// connection cannot be mistaken for an address space that lost its nodes.
// Errors that concern a single node (unknown node, access denied) skip that
// node only.
func BrowseVariables(ctx context.Context, client *opcua.Client, roots []*ua.NodeID, maxDepth, maxNodes int, namespaces []string) (BrowseResult, error) {
	return browseTree(ctx, &sessionSpace{client: client, namespaces: namespaces}, roots, maxDepth, maxNodes, namespaces)
}

// nodeRef is a node as a parent's Browse result describes it.
type nodeRef struct {
	ID     *ua.NodeID
	Name   string
	NameNS uint16 // namespace index of the BrowseName
	Class  ua.NodeClass
}

// varAttrs are the attributes read for a Variable node.
type varAttrs struct {
	Unreadable      bool // the read failed for this node; it is skipped
	Description     string
	AccessLevel     *uint8
	UserAccessLevel *uint8
	DataType        string
}

// addressSpace is what the walker needs from a session. sessionSpace
// implements it with batched service calls; tests use an in-memory tree.
type addressSpace interface {
	// Describe reads NodeClass and BrowseName of a root node.
	Describe(ctx context.Context, id *ua.NodeID) (nodeRef, error)
	// Children returns the forward HasComponent, Organizes and HasProperty
	// targets of each node, in that reference order.
	Children(ctx context.Context, ids []*ua.NodeID) ([][]nodeRef, error)
	// VariableAttributes reads description, access levels and data type.
	VariableAttributes(ctx context.Context, ids []*ua.NodeID) ([]varAttrs, error)
}

func browseTree(ctx context.Context, as addressSpace, roots []*ua.NodeID, maxDepth, maxNodes int, namespaces []string) (BrowseResult, error) {
	if maxDepth <= 0 {
		maxDepth = 10
	}
	if maxNodes <= 0 {
		maxNodes = 1000
	}
	w := &walker{as: as, maxDepth: maxDepth, maxNodes: maxNodes, visited: map[string]bool{}, namespaces: namespaces}
	for _, root := range roots {
		if w.truncated {
			break
		}
		key := root.String()
		if w.visited[key] {
			continue
		}
		w.visited[key] = true
		ref, err := as.Describe(ctx, root)
		if err != nil {
			return BrowseResult{}, fmt.Errorf("browse root %s unreadable: %w", key, err)
		}
		var attrs varAttrs
		if ref.Class == ua.NodeClassVariable {
			a, err := as.VariableAttributes(ctx, []*ua.NodeID{root})
			if err != nil {
				return BrowseResult{}, fmt.Errorf("browse root %s unreadable: %w", key, err)
			}
			attrs = a[0]
		}
		var kids []nodeRef
		if maxDepth >= 1 {
			k, err := as.Children(ctx, []*ua.NodeID{root})
			if err != nil {
				return BrowseResult{}, fmt.Errorf("browse root %s: %w", key, err)
			}
			kids = k[0]
		}
		w.rootNorm = inventory.NormalizeNodeID(root.String(), w.uri(root.Namespace()))
		if err := w.enter(ctx, ref, "", "", 0, attrs, kids); err != nil {
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
	requests := 0
	if c, isCounter := as.(interface{ requestCount() int }); isCounter {
		requests = c.requestCount()
	}
	return BrowseResult{Nodes: out, Truncated: w.truncated, Visited: len(w.visited), Requests: requests}, nil
}

type walker struct {
	as         addressSpace
	maxDepth   int
	maxNodes   int
	visited    map[string]bool
	namespaces []string
	out        []VariableNode
	truncated  bool
	rootNorm   string // NodeId of the current browse root in URI form
}

func (w *walker) uri(ns uint16) string {
	if int(ns) < len(w.namespaces) {
		return w.namespaces[ns]
	}
	return ""
}

// segment is one level of a normalized path: the BrowseName qualified with
// its namespace URI (or index, when the server did not name the URI).
func (w *walker) segment(ref nodeRef) string {
	u := w.uri(ref.NameNS)
	if u == "" {
		u = fmt.Sprintf("ns%d", ref.NameNS)
	}
	return "/{" + u + "}" + ref.Name
}

// canonicalOrder sorts children by BrowseName (with namespace URI) and then
// by NodeId in URI form. The walk therefore visits nodes in an order that
// depends on the address space, not on the order in which the server
// returns references: which path a node with several parents keeps, and
// which variables a truncated browse contains, stay the same when the server
// only reorders.
func (w *walker) canonicalOrder(kids []nodeRef) []nodeRef {
	type keyed struct {
		ref      nodeRef
		name, id string
	}
	ks := make([]keyed, len(kids))
	for i, k := range kids {
		ks[i] = keyed{k, w.segment(k), inventory.NormalizeNodeID(k.ID.String(), w.uri(k.ID.Namespace()))}
	}
	sort.SliceStable(ks, func(i, j int) bool {
		if ks[i].name != ks[j].name {
			return ks[i].name < ks[j].name
		}
		return ks[i].id < ks[j].id
	})
	out := make([]nodeRef, len(ks))
	for i, k := range ks {
		out[i] = k.ref
	}
	return out
}

// enter processes a node that was just marked visited at depth level. kids
// are its prefetched children (nil when they would lie beyond maxDepth).
// parentNorm is the parent's normalized path, empty for a browse root.
func (w *walker) enter(ctx context.Context, ref nodeRef, parentPath, parentNorm string, level int, attrs varAttrs, kids []nodeRef) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	path := joinPath(parentPath, ref.Name)
	norm := w.rootNorm
	if parentNorm != "" {
		norm = parentNorm + w.segment(ref)
	}
	if ref.Class == ua.NodeClassVariable {
		w.out = append(w.out, w.variable(ref, path, norm, attrs))
		if len(w.out) >= w.maxNodes {
			w.truncated = true
			return nil
		}
	}
	if len(kids) == 0 {
		return nil
	}

	// Prefetch for the children that are not visited yet: their own
	// children (when still within maxDepth) and their variable attributes.
	childLevel := level + 1
	pending := make([]nodeRef, 0, len(kids))
	seen := map[string]bool{}
	for _, k := range w.canonicalOrder(kids) {
		key := k.ID.String()
		if w.visited[key] || seen[key] {
			continue
		}
		seen[key] = true
		pending = append(pending, k)
	}
	var grandkids [][]nodeRef
	if childLevel+1 <= w.maxDepth && len(pending) > 0 {
		ids := make([]*ua.NodeID, len(pending))
		for i, k := range pending {
			ids[i] = k.ID
		}
		var err error
		if grandkids, err = w.as.Children(ctx, ids); err != nil {
			return fmt.Errorf("browse below %s: %w", path, err)
		}
	}
	attrsByKey := map[string]varAttrs{}
	var varIDs []*ua.NodeID
	for _, k := range pending {
		if k.Class == ua.NodeClassVariable {
			varIDs = append(varIDs, k.ID)
		}
	}
	if len(varIDs) > 0 {
		got, err := w.as.VariableAttributes(ctx, varIDs)
		if err != nil {
			return fmt.Errorf("read below %s: %w", path, err)
		}
		for i, id := range varIDs {
			attrsByKey[id.String()] = got[i]
		}
	}

	for i, k := range pending {
		key := k.ID.String()
		if w.visited[key] {
			continue // entered meanwhile through an earlier sibling's subtree
		}
		a := attrsByKey[key]
		if a.Unreadable {
			log.Printf("browse: skip unreadable node=%s", key)
			continue
		}
		w.visited[key] = true
		var next []nodeRef
		if grandkids != nil {
			next = grandkids[i]
		}
		if err := w.enter(ctx, k, path, norm, childLevel, a, next); err != nil {
			return err
		}
		if w.truncated {
			return nil
		}
	}
	return nil
}

func (w *walker) variable(ref nodeRef, path, norm string, a varAttrs) VariableNode {
	item := inventory.Item{
		NodeID:         ref.ID.String(),
		NamespaceIndex: ref.ID.Namespace(),
		NodeClass:      ref.Class.String(),
		BrowseName:     ref.Name,
		BrowsePath:     path,
		NormalizedPath: norm,
		Description:    a.Description,
		DataType:       a.DataType,
	}
	if int(item.NamespaceIndex) < len(w.namespaces) {
		item.NamespaceURI = w.namespaces[item.NamespaceIndex]
	}
	if a.AccessLevel != nil {
		item.AccessLevel = *a.AccessLevel
		item.Writable = ua.AccessLevelType(*a.AccessLevel)&ua.AccessLevelTypeCurrentWrite != 0
	}
	if a.UserAccessLevel != nil {
		item.UserAccessLevel = *a.UserAccessLevel
		item.UserWritable = ua.AccessLevelType(*a.UserAccessLevel)&ua.AccessLevelTypeCurrentWrite != 0
	}
	return VariableNode{NodeID: ref.ID, Item: item}
}

// Batch sizes stay well below common server operation limits
// (MaxNodesPerBrowse, MaxNodesPerRead); three browse descriptions and four
// read values are sent per node.
const (
	browseBatch = 32
	readBatch   = 64
)

var childReferenceTypes = []uint32{id.HasComponent, id.Organizes, id.HasProperty}

// sessionSpace implements addressSpace on an OPC UA session.
type sessionSpace struct {
	client     *opcua.Client
	namespaces []string
	requests   int
}

func (s *sessionSpace) requestCount() int { return s.requests }

func (s *sessionSpace) Describe(ctx context.Context, nid *ua.NodeID) (nodeRef, error) {
	s.requests++
	res, err := s.client.Read(ctx, &ua.ReadRequest{
		NodesToRead: []*ua.ReadValueID{
			{NodeID: nid, AttributeID: ua.AttributeIDNodeClass},
			{NodeID: nid, AttributeID: ua.AttributeIDBrowseName},
		},
		TimestampsToReturn: ua.TimestampsToReturnNeither,
	})
	if err != nil {
		return nodeRef{}, err
	}
	if len(res.Results) != 2 {
		return nodeRef{}, fmt.Errorf("read returned %d results for 2 attributes", len(res.Results))
	}
	if res.Results[0].Status != ua.StatusOK {
		return nodeRef{}, res.Results[0].Status
	}
	ref := nodeRef{ID: nid, Name: nid.String()}
	if v, isInt := variantUint(res.Results[0].Value); isInt {
		ref.Class = ua.NodeClass(v)
	}
	if ok(res.Results[1]) {
		if qn, isQN := res.Results[1].Value.Value().(*ua.QualifiedName); isQN && qn != nil {
			ref.Name, ref.NameNS = qn.Name, qn.NamespaceIndex
		} else {
			ref.Name = stripNamespacePrefix(res.Results[1].Value.String())
		}
	}
	return ref, nil
}

func (s *sessionSpace) Children(ctx context.Context, ids []*ua.NodeID) ([][]nodeRef, error) {
	out := make([][]nodeRef, len(ids))
	for start := 0; start < len(ids); start += browseBatch {
		end := min(start+browseBatch, len(ids))
		if err := s.browseChunk(ctx, ids[start:end], out[start:end]); err != nil {
			if IsConnectionError(err) || end-start == 1 {
				if IsConnectionError(err) {
					return nil, err
				}
				log.Printf("browse: children failed node=%s err=%v", ids[start], err)
				continue
			}
			// A service error that is not about the connection (for example
			// too many operations): retry node by node rather than lose a
			// whole batch of subtrees.
			for i := start; i < end; i++ {
				if err := s.browseChunk(ctx, ids[i:i+1], out[i:i+1]); err != nil {
					if IsConnectionError(err) {
						return nil, err
					}
					log.Printf("browse: children failed node=%s err=%v", ids[i], err)
				}
			}
		}
	}
	return out, nil
}

func (s *sessionSpace) browseChunk(ctx context.Context, ids []*ua.NodeID, out [][]nodeRef) error {
	descs := make([]*ua.BrowseDescription, 0, len(ids)*len(childReferenceTypes))
	for _, nid := range ids {
		for _, rt := range childReferenceTypes {
			descs = append(descs, &ua.BrowseDescription{
				NodeID:          nid,
				BrowseDirection: ua.BrowseDirectionForward,
				ReferenceTypeID: ua.NewNumericNodeID(0, rt),
				IncludeSubtypes: true,
				NodeClassMask:   uint32(ua.NodeClassAll),
				ResultMask:      uint32(ua.BrowseResultMaskAll),
			})
		}
	}
	s.requests++
	resp, err := s.client.Browse(ctx, &ua.BrowseRequest{
		View:                          &ua.ViewDescription{ViewID: ua.NewTwoByteNodeID(0)},
		RequestedMaxReferencesPerNode: 0,
		NodesToBrowse:                 descs,
	})
	if err != nil {
		return err
	}
	if len(resp.Results) != len(descs) {
		return fmt.Errorf("browse returned %d results for %d descriptions", len(resp.Results), len(descs))
	}
	for i, r := range resp.Results {
		n := i / len(childReferenceTypes)
		if r.StatusCode != ua.StatusOK {
			log.Printf("browse: children failed node=%s ref=%d status=%s", ids[n], childReferenceTypes[i%len(childReferenceTypes)], r.StatusCode)
			continue
		}
		refs := r.References
		cp := r.ContinuationPoint
		for len(cp) > 0 {
			s.requests++
			next, err := s.client.BrowseNext(ctx, &ua.BrowseNextRequest{ContinuationPoints: [][]byte{cp}})
			if err != nil {
				return err
			}
			if len(next.Results) != 1 || next.Results[0].StatusCode != ua.StatusOK {
				return fmt.Errorf("browse next for %s failed", ids[n])
			}
			refs = append(refs, next.Results[0].References...)
			cp = next.Results[0].ContinuationPoint
		}
		for _, ref := range refs {
			if ref == nil || ref.NodeID == nil || ref.NodeID.NodeID == nil || ref.NodeID.ServerIndex != 0 {
				continue
			}
			name, nameNS := ref.NodeID.NodeID.String(), uint16(0)
			if ref.BrowseName != nil {
				name, nameNS = ref.BrowseName.Name, ref.BrowseName.NamespaceIndex
			}
			out[n] = append(out[n], nodeRef{ID: ref.NodeID.NodeID, Name: name, NameNS: nameNS, Class: ref.NodeClass})
		}
	}
	return nil
}

var variableAttributeIDs = []ua.AttributeID{ua.AttributeIDDescription, ua.AttributeIDAccessLevel, ua.AttributeIDUserAccessLevel, ua.AttributeIDDataType}

func (s *sessionSpace) VariableAttributes(ctx context.Context, ids []*ua.NodeID) ([]varAttrs, error) {
	out := make([]varAttrs, len(ids))
	for start := 0; start < len(ids); start += readBatch {
		end := min(start+readBatch, len(ids))
		if err := s.readChunk(ctx, ids[start:end], out[start:end]); err != nil {
			if IsConnectionError(err) {
				return nil, err
			}
			for i := start; i < end; i++ {
				if err := s.readChunk(ctx, ids[i:i+1], out[i:i+1]); err != nil {
					if IsConnectionError(err) {
						return nil, err
					}
					log.Printf("browse: attributes failed node=%s err=%v", ids[i], err)
					out[i].Unreadable = true
				}
			}
		}
	}
	return out, nil
}

func (s *sessionSpace) readChunk(ctx context.Context, ids []*ua.NodeID, out []varAttrs) error {
	req := &ua.ReadRequest{TimestampsToReturn: ua.TimestampsToReturnNeither}
	for _, nid := range ids {
		for _, a := range variableAttributeIDs {
			req.NodesToRead = append(req.NodesToRead, &ua.ReadValueID{NodeID: nid, AttributeID: a})
		}
	}
	s.requests++
	res, err := s.client.Read(ctx, req)
	if err != nil {
		return err
	}
	if len(res.Results) != len(req.NodesToRead) {
		return fmt.Errorf("read returned %d results for %d attributes", len(res.Results), len(req.NodesToRead))
	}
	for i := range ids {
		r := res.Results[i*len(variableAttributeIDs):]
		a := varAttrs{}
		if ok(r[0]) {
			if lt, isLT := r[0].Value.Value().(*ua.LocalizedText); isLT && lt != nil {
				a.Description = strings.TrimSpace(lt.Text)
			} else {
				a.Description = strings.TrimSpace(r[0].Value.String())
			}
		}
		if ok(r[1]) {
			if v, isInt := variantUint(r[1].Value); isInt {
				b := uint8(v)
				a.AccessLevel = &b
			}
		}
		if ok(r[2]) {
			if v, isInt := variantUint(r[2].Value); isInt {
				b := uint8(v)
				a.UserAccessLevel = &b
			}
		}
		if ok(r[3]) {
			if nid, isNID := r[3].Value.Value().(*ua.NodeID); isNID {
				a.DataType = DataTypeName(nid, s.namespaces...)
			}
		} else if r[3] != nil {
			if NodeGone(r[3].Status) {
				// Deleted between Browse and Read: it is not part of the
				// address space any more, so it is not part of the inventory.
				log.Printf("browse: node=%s vanished during the browse (%s), skipped", ids[i], StatusName(r[3].Status))
				a.Unreadable = true
			} else {
				log.Printf("browse: node=%s data type unreadable (%s), recorded without data type", ids[i], StatusName(r[3].Status))
			}
		}
		out[i] = a
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
// every other type to its NodeId string, in URI form ("nsu=...;i=3004") when
// the namespace array is given, so that a renumbered namespace does not
// change it.
func DataTypeName(nodeID *ua.NodeID, namespaces ...string) string {
	if nodeID == nil {
		return ""
	}
	if ns := nodeID.Namespace(); ns != 0 {
		if int(ns) < len(namespaces) {
			return inventory.NormalizeNodeID(nodeID.String(), namespaces[ns])
		}
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
	case id.GUID:
		return "guid"
	case id.XMLElement:
		return "xmlelement"
	case id.ExpandedNodeID:
		return "expandednodeid"
	case id.StatusCode:
		return "statuscode"
	case id.QualifiedName:
		return "qualifiedname"
	case id.Structure:
		return "structure"
	case id.DataValue:
		return "datavalue"
	case id.BaseDataType:
		return "variant"
	case id.DiagnosticInfo:
		return "diagnosticinfo"
	case id.Number:
		return "number"
	case id.Integer:
		return "integer"
	case id.UInteger:
		return "uinteger"
	case id.Enumeration:
		return "enumeration"
	case id.Decimal:
		return "decimal"
	default:
		return nodeID.String()
	}
}

// ResolveNodeID parses a NodeId string. Besides the usual forms ("ns=2;s=X",
// "i=85") it accepts "nsu=<namespace uri>;<identifier>", resolved against the
// server's namespace array, so a configuration survives a server that
// renumbers its namespaces.
func ResolveNodeID(s string, namespaces []string) (*ua.NodeID, error) {
	if !strings.HasPrefix(s, "nsu=") {
		return ua.ParseNodeID(s)
	}
	semi := strings.Index(s, ";")
	if semi < 0 {
		return nil, fmt.Errorf("%q: nsu= needs ;<identifier>", s)
	}
	uri, rest := s[len("nsu="):semi], s[semi+1:]
	for i, u := range namespaces {
		if u == uri {
			if i == 0 {
				return ua.ParseNodeID(rest)
			}
			return ua.ParseNodeID(fmt.Sprintf("ns=%d;%s", i, rest))
		}
	}
	return nil, fmt.Errorf("%q: namespace %s is not in the server's namespace array", s, uri)
}
