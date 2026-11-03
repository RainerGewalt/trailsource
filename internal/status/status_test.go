package status

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestInventoryClassification(t *testing.T) {
	cases := []struct {
		initial                 bool
		added, removed, changed int
		want                    string
	}{
		{true, 5, 0, 0, InventoryInitial},
		{false, 2, 0, 0, NodeAdded},
		{false, 0, 3, 0, NodeRemoved},
		{false, 0, 0, 1, NodeChanged},
		{false, 1, 1, 0, NodeRemoved},
		{false, 1, 0, 4, NodeChanged},
		{false, 1, 2, 3, NodeRemoved},
	}
	for _, c := range cases {
		if got := Inventory(c.initial, c.added, c.removed, c.changed); got != c.want {
			t.Errorf("Inventory(%v,%d,%d,%d) = %s, want %s", c.initial, c.added, c.removed, c.changed, got, c.want)
		}
	}
}

func TestQuality(t *testing.T) {
	if Quality("bad") != QualityBad || Quality("uncertain") != QualityUncertain || Quality("good") != "" {
		t.Fatal("quality codes wrong")
	}
}

func TestCanonicalTranslatesProvisionalNames(t *testing.T) {
	for old, want := range Provisional() {
		if got, ok := Canonical(old); !ok || got != want {
			t.Errorf("Canonical(%s) = %s %v, want %s", old, got, ok, want)
		}
	}
	if _, ok := Canonical("source.inventory.mixed"); ok {
		t.Error("mixed has no single catalog code")
	}
	if got, ok := Canonical(NodeAdded); !ok || got != NodeAdded {
		t.Error("catalog names pass through")
	}
}

// Catalog pattern <namespace>.<name>: one dot, then lowercase and underscores.
var catalogPattern = regexp.MustCompile(`^(source|common)\.[a-z][a-z0-9_]*$`)

// Every code follows the catalog pattern and is documented in docs/STATUS.md.
func TestCodesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/STATUS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range Codes {
		if !catalogPattern.MatchString(c) {
			t.Errorf("%s does not follow the catalog pattern <namespace>.<name>", c)
		}
		if !strings.Contains(string(doc), "`"+c+"`") {
			t.Errorf("%s is not documented in docs/STATUS.md", c)
		}
	}
}

// When the trail-evidence repository is checked out next to this one, every
// code must be in its catalog. CI without the sibling skips this test.
func TestCodesAreInTheTrailEvidenceCatalog(t *testing.T) {
	raw, err := os.ReadFile("../../../trail-evidence/schema/reason-codes.json")
	if err != nil {
		t.Skip("trail-evidence not checked out next to this repository")
	}
	var cat struct {
		Codes []struct {
			Code string `json:"code"`
		} `json:"codes"`
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, c := range cat.Codes {
		known[c.Code] = true
	}
	for _, c := range Codes {
		if !known[c] {
			t.Errorf("%s is not in trail-evidence/schema/reason-codes.json", c)
		}
	}
}
