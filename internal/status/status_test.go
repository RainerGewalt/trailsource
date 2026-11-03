package status

import (
	"os"
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
		{false, 2, 0, 0, InventoryAdded},
		{false, 0, 3, 0, InventoryRemoved},
		{false, 0, 0, 1, InventoryModified},
		{false, 1, 1, 0, InventoryMixed},
		{false, 1, 0, 4, InventoryMixed},
	}
	for _, c := range cases {
		if got := Inventory(c.initial, c.added, c.removed, c.changed); got != c.want {
			t.Errorf("Inventory(%v,%d,%d,%d) = %s, want %s", c.initial, c.added, c.removed, c.changed, got, c.want)
		}
	}
}

// Every code must use one of the two namespaces and appear in docs/STATUS.md.
func TestCodesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/STATUS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range Codes {
		if !strings.HasPrefix(c, "source.") && !strings.HasPrefix(c, "common.") {
			t.Errorf("%s is outside the source.* and common.* namespaces", c)
		}
		if !strings.Contains(string(doc), "`"+c+"`") {
			t.Errorf("%s is not documented in docs/STATUS.md", c)
		}
	}
}
