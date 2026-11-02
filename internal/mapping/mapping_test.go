package mapping

import (
	"os"
	"path/filepath"
	"testing"
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
