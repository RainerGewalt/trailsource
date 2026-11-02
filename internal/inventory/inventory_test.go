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
		t.Fatal("fingerprint covers only NodeId, BrowseName and DataType")
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
