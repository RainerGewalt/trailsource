package opcua

import (
	"testing"
	"time"

	"github.com/gopcua/opcua/ua"
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
