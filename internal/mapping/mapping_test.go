package mapping

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RainerGewalt/trailsource/internal/inventory"
)

func TestResolveAndDerive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.yaml")
	os.WriteFile(p, []byte(`
version: "2026-11"
rules:
  - path: Plant.Line01.Extruder01.MeltPressureBar
    semantic_role: melt_pressure
    unit: BAR
  - pattern: Plant.Line01.Extruder01.BarrelZone*.TempRaw
    semantic_role: barrel_zone_temp_raw
    scale_decimals: 1
    unit: CEL
`), 0o644)
	f, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if f.Hash == "" || f.Version != "2026-11" {
		t.Fatalf("file meta: %+v", f)
	}
	r := f.Resolve("Plant.Line01.Extruder01.MeltPressureBar", "MeltPressureBar")
	if r.SemanticRole != "melt_pressure" || r.Source != "mapping" || r.Unit != "BAR" {
		t.Fatalf("exact: %+v", r)
	}
	r = f.Resolve("Plant.Line01.Extruder01.BarrelZone03.TempRaw", "TempRaw")
	if r.SemanticRole != "barrel_zone_temp_raw" || r.ScaleDecimals == nil || *r.ScaleDecimals != 1 {
		t.Fatalf("pattern: %+v", r)
	}
	r = f.Resolve("Plant.Line01.Extruder01.ScrewSpeedRpm", "ScrewSpeedRpm")
	if r.SemanticRole != "screw_speed_rpm" || r.Source != "derived" {
		t.Fatalf("derived: %+v", r)
	}
	if DerivedRole("", "HaulOffSpeedMPerMin") != "haul_off_speed_m_per_min" {
		t.Fatal(DerivedRole("", "HaulOffSpeedMPerMin"))
	}
}

func TestSkeletonLoadsAndResolves(t *testing.T) {
	items := []inventory.Item{
		{NodeID: "ns=2;s=Plant.Line01.Extruder01.MeltPressureBar", BrowsePath: "Plant.Line01.Extruder01.MeltPressureBar", BrowseName: "MeltPressureBar", DataType: "float64"},
		{NodeID: "ns=3;s=Demo: #weird", BrowsePath: "Demo.Static.Value: #1", BrowseName: "Value: #1", DataType: "int32", UserWritable: true},
	}
	raw, err := Skeleton(items, "opc.tcp://example:4840", "2026-11-03T08:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mapping.yaml")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("skeleton does not load: %v\n%s", err, raw)
	}
	if len(f.Rules) != 2 || f.Version != "1" {
		t.Fatalf("unexpected skeleton: %+v", f)
	}
	r := f.Resolve("Demo.Static.Value: #1", "Value: #1")
	if r.Source != "mapping" || r.SemanticRole != DerivedRole("Demo.Static.Value: #1", "Value: #1") {
		t.Fatalf("special characters not preserved: %+v\n%s", r, raw)
	}
	if !strings.Contains(string(raw), "# int32, rw, ns=3;s=Demo: #weird") {
		t.Fatalf("line comment missing:\n%s", raw)
	}
}
