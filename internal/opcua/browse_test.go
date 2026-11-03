package opcua

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/gopcua/opcua/ua"
)

// A browse on a dying session must abort instead of sealing an empty or
// partial inventory; only errors about one node may be skipped.
func TestIsConnectionError(t *testing.T) {
	abort := []error{
		ua.StatusBadServerNotConnected,
		ua.StatusBadConnectionClosed,
		ua.StatusBadSessionIDInvalid,
		ua.StatusBadSecureChannelClosed,
		ua.StatusBadTimeout,
		fmt.Errorf("read: %w", ua.StatusBadServerNotConnected),
		io.EOF,
		context.DeadlineExceeded,
		errors.New("dial tcp 127.0.0.1:4840: connect: connection refused"),
	}
	for _, err := range abort {
		if !IsConnectionError(err) {
			t.Errorf("%v: want connection error (abort browse)", err)
		}
	}
	skip := []error{
		ua.StatusBadNodeIDUnknown,
		ua.StatusBadUserAccessDenied,
		fmt.Errorf("attrs: %w", ua.StatusBadNotReadable),
	}
	for _, err := range skip {
		if IsConnectionError(err) {
			t.Errorf("%v: want node-level error (skip node)", err)
		}
	}
	if IsConnectionError(nil) {
		t.Error("nil is not an error")
	}
}

// fakeSpace is an in-memory address space. Node ids are their string names.
type fakeSpace struct {
	class    map[string]ua.NodeClass
	children map[string][]string
	fail     map[string]error // VariableAttributes error per node
	childErr error            // returned by every Children call when set
	calls    []int            // batch size of every Children call
}

func (f *fakeSpace) ref(name string) nodeRef {
	short := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		short = name[i+1:]
	}
	return nodeRef{ID: ua.NewStringNodeID(1, name), Name: short, Class: f.class[name]}
}

func (f *fakeSpace) Describe(_ context.Context, id *ua.NodeID) (nodeRef, error) {
	if _, known := f.class[id.StringID()]; !known {
		return nodeRef{}, ua.StatusBadNodeIDUnknown
	}
	return f.ref(id.StringID()), nil
}

func (f *fakeSpace) Children(_ context.Context, ids []*ua.NodeID) ([][]nodeRef, error) {
	f.calls = append(f.calls, len(ids))
	if f.childErr != nil {
		return nil, f.childErr
	}
	out := make([][]nodeRef, len(ids))
	for i, id := range ids {
		for _, c := range f.children[id.StringID()] {
			out[i] = append(out[i], f.ref(c))
		}
	}
	return out, nil
}

func (f *fakeSpace) VariableAttributes(_ context.Context, ids []*ua.NodeID) ([]varAttrs, error) {
	out := make([]varAttrs, len(ids))
	for i, id := range ids {
		if err := f.fail[id.StringID()]; err != nil {
			if IsConnectionError(err) {
				return nil, err
			}
			out[i].Unreadable = true
			continue
		}
		out[i].DataType = "float64"
	}
	return out, nil
}

// root
// ├── a (object)
// │   ├── a1 (var)  ── a1p (var, property of a1)
// │   ├── shared (var)
// │   └── deep (object) ── deeper (object) ── tooDeep (var)
// └── b (object)
//
//	├── shared (var, second path: keeps the first)
//	└── b1 (var)
func newFakeSpace() *fakeSpace {
	O, V := ua.NodeClassObject, ua.NodeClassVariable
	return &fakeSpace{
		class: map[string]ua.NodeClass{"root": O, "a": O, "a1": V, "a1p": V, "shared": V, "deep": O, "deeper": O, "tooDeep": V, "b": O, "b1": V},
		children: map[string][]string{
			"root": {"a", "b"}, "a": {"a1", "shared", "deep"}, "a1": {"a1p"}, "deep": {"deeper"}, "deeper": {"tooDeep"}, "b": {"shared", "b1"},
		},
		fail: map[string]error{},
	}
}

func paths(res BrowseResult) []string {
	var out []string
	for _, n := range res.Nodes {
		out = append(out, n.Item.BrowsePath)
	}
	return out
}

func TestBrowseTreeDepthFirstAndDedup(t *testing.T) {
	f := newFakeSpace()
	res, err := browseTree(context.Background(), f, []*ua.NodeID{ua.NewStringNodeID(1, "root")}, 3, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"root.a.a1", "root.a.a1.a1p", "root.a.shared", "root.b.b1"}
	if strings.Join(paths(res), ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v (tooDeep lies at depth 4)", paths(res), want)
	}
	if res.Truncated {
		t.Fatal("not truncated")
	}
	// siblings are browsed together: root alone, then a+b, then a1+shared+deep
	batched := false
	for _, n := range f.calls {
		batched = batched || n > 1
	}
	if !batched {
		t.Fatalf("children were not batched: %v", f.calls)
	}
}

func TestBrowseTreeTruncatesInDepthFirstOrder(t *testing.T) {
	res, err := browseTree(context.Background(), newFakeSpace(), []*ua.NodeID{ua.NewStringNodeID(1, "root")}, 10, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || strings.Join(paths(res), ",") != "root.a.a1,root.a.a1.a1p" {
		t.Fatalf("want the first two variables in depth-first order, got %v truncated=%v", paths(res), res.Truncated)
	}
}

func TestBrowseTreeErrors(t *testing.T) {
	root := []*ua.NodeID{ua.NewStringNodeID(1, "root")}

	f := newFakeSpace()
	f.fail["shared"] = ua.StatusBadUserAccessDenied
	res, err := browseTree(context.Background(), f, root, 3, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths(res), ",") != "root.a.a1,root.a.a1.a1p,root.b.b1" {
		t.Fatalf("an unreadable node is skipped, the rest stays: %v", paths(res))
	}

	f = newFakeSpace()
	f.fail["b1"] = ua.StatusBadServerNotConnected
	if _, err := browseTree(context.Background(), f, root, 3, 100, nil); err == nil {
		t.Fatal("a connection error must abort the browse")
	}

	f = newFakeSpace()
	f.childErr = ua.StatusBadSessionClosed
	if _, err := browseTree(context.Background(), f, root, 3, 100, nil); err == nil {
		t.Fatal("a session error while browsing children must abort the browse")
	}

	if _, err := browseTree(context.Background(), newFakeSpace(), []*ua.NodeID{ua.NewStringNodeID(1, "missing")}, 3, 100, nil); err == nil {
		t.Fatal("an unknown root must abort the browse")
	}
}

func TestNodeGoneTriggersOnlyOnDeletedNodes(t *testing.T) {
	if !NodeGone(ua.StatusBadNodeIDUnknown) || !NodeGone(ua.StatusBadNodeIDInvalid) {
		t.Fatal("deleted node not recognised")
	}
	for _, c := range []ua.StatusCode{ua.StatusOK, ua.StatusBadWaitingForInitialData, ua.StatusBadCommunicationError, ua.StatusUncertain} {
		if NodeGone(c) {
			t.Errorf("%s must not trigger a re-browse", c)
		}
	}
}

func TestCheckPinned(t *testing.T) {
	got := "fa2042bc09ab10367af1d193405c3f25759ba023"
	if err := checkPinned("", got); err != nil {
		t.Fatal("no pin must accept any certificate")
	}
	if err := checkPinned("FA:20:42:BC:09:AB:10:36:7A:F1:D1:93:40:5C:3F:25:75:9B:A0:23", got); err != nil {
		t.Fatalf("colons and case must be ignored: %v", err)
	}
	if err := checkPinned("0000000000000000000000000000000000000000", got); err == nil {
		t.Fatal("a different certificate must be refused")
	}
	if err := checkPinned(got, ""); err == nil {
		t.Fatal("a pin on an unsecured session must be refused")
	}
}
