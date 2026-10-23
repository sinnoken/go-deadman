package presets

import (
	"slices"
	"testing"
)

func TestNamesAndGet(t *testing.T) {
	names := Names()
	if !slices.Equal(names, []string{"internet", "tasks"}) {
		t.Fatalf("Names() = %v", names)
	}
	if !slices.Contains(names, Default) {
		t.Fatalf("Default %q not embedded", Default)
	}
	for _, n := range names {
		if data, err := Get(n); err != nil || len(data) == 0 {
			t.Errorf("Get(%q): %v", n, err)
		}
	}
	if _, err := Get("nope"); err == nil {
		t.Error("unknown preset should fail")
	}
	if _, err := Get("../go"); err == nil {
		t.Error("path traversal should fail")
	}
}
