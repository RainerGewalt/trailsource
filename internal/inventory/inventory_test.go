package inventory

import (
	"path/filepath"
	"testing"
)

func items() []Item {
	return []Item{
		{NodeID: "ns=2;s=A.Zone01.Temp", BrowsePath: "A.Zone01.Temp", BrowseName: "Temp", DataType: "float64", AccessLevel: 3, UserAccessLevel: 3, Writable: true},
		{NodeID: "ns=2;s=A.Pressure", BrowsePath: "A.Pressure", BrowseName: "Pressure", DataType: "float64", AccessLevel: 1, UserAccessLevel: 1},
	}
}

func TestFingerprintIsOrderIndependentAndSensitive(t *testing.T) {
	a := items()
	b := []Item{a[1], a[0]}
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("fingerprint must not depend on item order")
	}
	c := items()
	c[0].Description = "irrelevant"
	c[0].AccessLevel = 1
	if Fingerprint(a) != Fingerprint(c) {
		t.Fatal("fingerprint ignores descriptions and access levels")
	}
	d := items()
	d[0].DataType = "int16"
	if Fingerprint(a) == Fingerprint(d) {
		t.Fatal("data type change must alter the fingerprint")
	}
	if fp := Fingerprint(a); len(fp) != 7+64 || fp[:7] != "sha256:" {
		t.Fatalf("fingerprint shape: %s", fp)
	}
}

func TestDiff(t *testing.T) {
	old := items()
	next := items()
	next[0].DataType = "int16"
	next[1].UserAccessLevel = 3
	next = append(next, Item{NodeID: "ns=2;s=A.New", BrowsePath: "A.New", BrowseName: "New", DataType: "bool"})
	next = next[1:] // drop Zone01.Temp -> removed; keep changed Pressure and added New

	d := Compute(old, next)
	if len(d.Added) != 1 || d.Added[0].NodeID != "ns=2;s=A.New" {
		t.Fatalf("added: %+v", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].NodeID != "ns=2;s=A.Zone01.Temp" {
		t.Fatalf("removed: %+v", d.Removed)
	}
	if len(d.Changed) != 1 || d.Changed[0].Field != "user_access_level" || d.Changed[0].New != "3" {
		t.Fatalf("changed: %+v", d.Changed)
	}
	if Compute(old, items()).Empty() != true {
		t.Fatal("identical snapshots must yield an empty diff")
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inv", "snap.json")
	if _, found, err := Load(path); err != nil || found {
		t.Fatalf("missing snapshot: found=%v err=%v", found, err)
	}
	s := &Snapshot{SourceID: "src", Generation: 2, Fingerprint: Fingerprint(items()), Items: items()}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, found, err := Load(path)
	if err != nil || !found || got.Generation != 2 || len(got.Items) != 2 || got.Fingerprint != s.Fingerprint {
		t.Fatalf("round trip failed: %+v %v %v", got, found, err)
	}
}

func TestEntityKey(t *testing.T) {
	if got := EntityKey("src-01", "Plant.Line01.Extruder01.BarrelZone01.TempActual", ""); got != "src-01/plant-line01-extruder01-barrelzone01-tempactual" {
		t.Fatalf("got %s", got)
	}
	if got := EntityKey("src", "", "ns=2;i=42"); got != "src/ns-2-i-42" {
		t.Fatalf("got %s", got)
	}
}

func TestNormalizeNodeID(t *testing.T) {
	if got := NormalizeNodeID("ns=3;s=Line1.Temp", "urn:x"); got != "nsu=urn:x;s=Line1.Temp" {
		t.Fatal(got)
	}
	if got := NormalizeNodeID("i=2258", "http://opcfoundation.org/UA/"); got != "i=2258" {
		t.Fatal(got)
	}
	a := Item{NodeID: "ns=2;s=X", NamespaceURI: "urn:x"}
	b := Item{NodeID: "ns=5;s=X", NamespaceURI: "urn:x"}
	if a.Key() != b.Key() {
		t.Fatal("same URI and identifier must give the same key")
	}
}

// Two variables whose browse paths collapse to the same entity_key (for
// example sibling BrowseNames that differ only in namespace) get distinct,
// order-independent keys.
func TestAssignEntityKeysHasNoDuplicates(t *testing.T) {
	mk := func() []Item {
		return []Item{
			{NodeID: "ns=2;s=A.Temp", NamespaceURI: "urn:a", BrowsePath: "A.Temp"},
			{NodeID: "ns=3;s=A.Temp", NamespaceURI: "urn:b", BrowsePath: "A.Temp"},
			{NodeID: "ns=2;s=A.Speed", NamespaceURI: "urn:a", BrowsePath: "A.Speed"},
		}
	}
	x := mk()
	AssignEntityKeys("src", x)
	y := mk()
	y[0], y[1] = y[1], y[0]
	AssignEntityKeys("src", y)
	seen := map[string]bool{}
	for _, it := range x {
		if seen[it.EntityKey] {
			t.Fatalf("duplicate entity_key %s", it.EntityKey)
		}
		seen[it.EntityKey] = true
	}
	if x[2].EntityKey != "src/a-speed" {
		t.Fatalf("unique paths keep the plain key, got %s", x[2].EntityKey)
	}
	byKey := func(items []Item) map[string]string {
		m := map[string]string{}
		for _, it := range items {
			m[it.Key()] = it.EntityKey
		}
		return m
	}
	bx, by := byKey(x), byKey(y)
	for k, v := range bx {
		if by[k] != v {
			t.Fatalf("collision suffix depends on order: %s %s vs %s", k, v, by[k])
		}
	}
}
