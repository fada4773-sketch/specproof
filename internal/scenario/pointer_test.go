package scenario

import "testing"

// TestLookup: a part in brackets finds the element of a list by its
// fields, also in the one list of a page; a saved value is escaped.
func TestLookup(t *testing.T) {
	ships := []any{
		map[string]any{"id": 1, "code": "A/1", "dock": "north"},
		map[string]any{"id": 2, "Code": "B-2", "dock": "south"},
	}
	page := map[string]any{"items": ships, "total": 2}
	src, missing := FillSource("/[code={{ship.code}}][dock=north]/id", map[string]any{"ship.code": "A/1"})
	if len(missing) > 0 || src != "/[code=A~11][dock=north]/id" {
		t.Fatalf("filled: %q %v", src, missing)
	}
	for _, c := range []struct {
		v    any
		ptr  string
		want any
		ok   bool
	}{
		{ships, src, 1, true},
		{page, src, 1, true},
		{page, "/items/[code=B-2]/id", 2, true},
		{ships, "/[code=B-2][dock=north]/id", nil, false},
		{ships, "/1/dock", "south", true},
		{map[string]any{"a": ships, "b": ships}, "/[code=B-2]/id", nil, false},
	} {
		got, ok := Lookup(c.v, c.ptr)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("%s: %v %v, want %v %v", c.ptr, got, ok, c.want, c.ok)
		}
	}
	if _, missing := FillSource("/[code={{x}}]/id", nil); len(missing) != 1 {
		t.Error("a missing value is named")
	}
}
