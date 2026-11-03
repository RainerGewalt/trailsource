package opcua

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/gopcua/opcua/ua"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

func TestEncodeValue(t *testing.T) {
	cases := []struct {
		in   any
		want any
		typ  string
	}{
		{float64(213.5), float64(213.5), "float64"},
		{float32(0.1), float64(0.1), "float32"},
		{float64(1e21), float64(1e21), "float64"},
		{int16(-7), int64(-7), "int16"},
		{uint32(42), uint64(42), "uint32"},
		{int64(9007199254740993), "9007199254740993", "int64"},
		{uint64(18446744073709551615), "18446744073709551615", "uint64"},
		{true, true, "bool"},
		{"x", "x", "string"},
		{time.Date(2026, 1, 2, 3, 4, 5, 600000000, time.UTC), "2026-01-02T03:04:05.6Z", "datetime"},
		{nil, nil, "null"},
	}
	for _, c := range cases {
		got, typ := EncodeValue(c.in)
		if got != c.want || typ != c.typ {
			t.Errorf("EncodeValue(%v) = %v,%s want %v,%s", c.in, got, typ, c.want, c.typ)
		}
	}
	arr, typ := EncodeValue([]float64{1.5, 2})
	if typ != "array<float64>" || arr.([]any)[0] != 1.5 || arr.([]any)[1] != 2.0 {
		t.Errorf("array: %v %s", arr, typ)
	}
}

func TestScaleInteger(t *testing.T) {
	if got := ScaleInteger(int64(2135), 1); got != "213.5" {
		t.Fatal(got)
	}
	if got := ScaleInteger(int64(-5), 2); got != "-0.05" {
		t.Fatal(got)
	}
	if got := ScaleInteger(uint64(7), 0); got != "7" {
		t.Fatal(got)
	}
	if got := ScaleInteger("9007199254740993", 3); got != "9007199254740.993" {
		t.Fatal(got)
	}
	if got := ScaleInteger("x", 1); got != nil {
		t.Fatal(got)
	}
}

func TestQuality(t *testing.T) {
	if Quality(ua.StatusOK) != "good" || Quality(ua.StatusUncertain) != "uncertain" || Quality(ua.StatusBadNodeIDUnknown) != "bad" {
		t.Fatal("quality classification wrong")
	}
	if StatusName(ua.StatusOK) != "StatusGood" {
		t.Fatal(StatusName(ua.StatusOK))
	}
}

type testStatus int32

type testStruct struct {
	Name    string
	Count   int64
	Ratio   float64
	Started time.Time
	Inner   *ua.LocalizedText
	hidden  int
}

// Types seen on a public reference server must encode to JSON that hashes
// under JCS, with large integers as strings and NaN as null.
func TestEncodeValueBuiltinAndStructuredTypes(t *testing.T) {
	guid := ua.NewGUID("72962B91-FA75-4AE6-8D28-B404DC7DAF63")
	ts := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		in       any
		want     string // JSON of the encoded value
		wantType string
	}{
		{"guid", guid, `"72962B91-FA75-4AE6-8D28-B404DC7DAF63"`, "guid"},
		{"xml", ua.XMLElement("x=1"), `"x=1"`, "xmlelement"},
		{"expanded", &ua.ExpandedNodeID{NodeID: ua.NewStringNodeID(2, "X"), NamespaceURI: "urn:x"}, `"nsu=urn:x;ns=2;s=X"`, "expandednodeid"},
		{"qualified", &ua.QualifiedName{NamespaceIndex: 2, Name: "Pump"}, `"2:Pump"`, "qualifiedname"},
		{"variant", ua.MustVariant(int64(9007199254740993)), `"9007199254740993"`, "variant<int64>"},
		{"datavalue", &ua.DataValue{Value: ua.MustVariant(float32(0.1)), Status: ua.StatusUncertain, SourceTimestamp: ts},
			`{"server_timestamp":null,"source_timestamp":"2026-11-03T08:00:00Z","status_code":"0x40000000","value":0.1,"value_type":"float32"}`, "datavalue"},
		{"extension decoded", &ua.ExtensionObject{TypeID: ua.NewFourByteExpandedNodeID(0, 296), Value: &testStruct{Name: "n", Count: 1 << 60, Ratio: math.NaN(), Started: ts, Inner: &ua.LocalizedText{Text: "t"}}},
			`{"body":{"Count":"1152921504606846976","Inner":"t","Name":"n","Ratio":null,"Started":"2026-11-03T08:00:00Z"},"type_id":"i=296"}`, "extensionobject"},
		{"extension raw", &ua.ExtensionObject{TypeID: ua.NewNumericExpandedNodeID(2, 5001), Value: []byte{1, 2}}, `{"body_base64":"AQI=","type_id":"ns=2;i=5001"}`, "extensionobject"},
		{"enum", testStatus(3), `3`, "enum:opcua.testStatus"},
		{"matrix", [][]int16{{1, 2}, {3, 4}}, `[[1,2],[3,4]]`, "array<array<int16>>"},
		{"empty array", []float64{}, `[]`, "array<float64>"},
	}
	for _, c := range cases {
		got, typ := EncodeValue(c.in)
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.name, err)
		}
		if string(raw) != c.want || typ != c.wantType {
			t.Errorf("%s: got %s (%s), want %s (%s)", c.name, raw, typ, c.want, c.wantType)
		}
		if _, err := evidence.Hash(map[string]any{"value": got}); err != nil {
			t.Errorf("%s: not canonicalisable: %v", c.name, err)
		}
	}
}
