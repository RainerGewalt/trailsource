package evidence

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateVectors = flag.Bool("update-vectors", false, "rewrite testdata/vectors.json from this implementation")

func TestGenerateVectors(t *testing.T) {
	if !*updateVectors {
		t.Skip("run with -update-vectors to regenerate")
	}
	v1 := baseRecord()

	v2 := baseRecord()
	v2.Seq = 5
	v2.EventType = "source.value.observed"
	v2.ChainRestart = true
	v2.AnchorHash = "sha256:abcd0000000000000000000000000000000000000000000000000000000000ef"
	v2.PrevHash = v2.AnchorHash
	v2.Time = Time{RecordedAt: "2026-01-01T00:00:01.250Z", OccurredAt: "2026-01-01T00:00:01.100000Z", ReadAt: "2026-01-01T00:00:01.200Z", Source: TimeSourceSystem}
	v2.Actor = Actor{Auth: ActorAuthSelfDeclared, ID: "trailsource-opcua-01", Roles: []string{"collector"}}
	v2.Policy = Policy{ID: "plant-a/addressspace", Version: "3", Hash: "sha256:1111111111111111111111111111111111111111111111111111111111111111", Kind: "addressspace", Status: "active"}
	v2.Payload = map[string]any{
		"native_node_id": "ns=2;s=Plant.Line01.Extruder01.MeltPressureBar",
		"value":          "187.4",
		"value_type":     "float64",
		"status_code":    "0x00000000",
		"nested":         map[string]any{"z": nil, "y": []any{true, false, 0}},
	}
	v2.Context = map[string]any{"ignored": "yes"}

	sealedV1 := v1
	if err := Seal(&sealedV1); err != nil {
		t.Fatal(err)
	}
	v3 := baseRecord()
	v3.Seq = 1
	v3.PrevHash = sealedV1.ChainHash
	v3.EventType = "source.inventory.changed"
	v3.Payload = map[string]any{
		"é":          "unicode key",
		"a":          "quote \" backslash \\ newline \n tab \t ctrl \u0001",
		"big":        int64(9007199254740993),
		"neg":        -42,
		"\U0001D11E": "astral key sorts before U+FF21 in UTF-16",
		"Ａ":          "fullwidth A",
		"empty":      map[string]any{},
		"list":       []any{},
	}

	type out struct {
		Name                string `json:"name"`
		Record              Record `json:"record"`
		ExpectedContentHash string `json:"expected_content_hash"`
		ExpectedChainHash   string `json:"expected_chain_hash"`
		ExpectedCoreJCS     string `json:"expected_core_jcs"`
	}
	var outs []out
	for _, c := range []struct {
		name string
		rec  Record
	}{{"genesis-minimal", v1}, {"restart-with-optional-fields", v2}, {"unicode-and-escaping", v3}} {
		rec := c.rec
		if err := Seal(&rec); err != nil {
			t.Fatal(err)
		}
		core, _ := CoreJCS(&rec)
		outs = append(outs, out{c.name, rec, rec.ContentHash, rec.ChainHash, string(core)})
	}
	b, _ := json.MarshalIndent(outs, "", "  ")
	path := filepath.Join("..", "..", "testdata", "vectors.json")
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
