package main

import "testing"

func TestNormalizeVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v0.1.0":                     "0.1.0",
		"0.1.0":                      "0.1.0",
		"v0.1.0-1-ge3a3431-dirty":    "0.1.0-1-ge3a3431-dirty",
		"v0.1.1-0.20261104120000-ab": "0.1.1-0.20261104120000-ab",
		"0.1.0-dev":                  "0.1.0-dev",
		"e3a3431":                    "e3a3431",
		"v":                          "v",
		"version":                    "version",
		"":                           "",
	} {
		if got := normalizeVersion(in); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
