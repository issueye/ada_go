//go:build windows

package win32

import "testing"

func TestKeyCodes(t *testing.T) {
	cases := []struct {
		chord     string
		wantMods  int
		wantKeys  []uint16
		wantError bool
	}{
		{"enter", 0, []uint16{0x0D}, false},
		{"ctrl+s", 1, []uint16{'S'}, false},
		{"ctrl+shift+t", 2, []uint16{'T'}, false},
		{"alt+tab", 1, []uint16{0x09}, false},
		{"win+r", 1, []uint16{'R'}, false},
		{"f5", 0, []uint16{0x74}, false},
		{"f24", 0, []uint16{0x87}, false},
		{"ctrl+-", 1, []uint16{0xBD}, false},
		{"ctrl+nope", 0, nil, true},
		{"ctrl", 0, nil, true},
		{"", 0, nil, true},
	}
	for _, c := range cases {
		mods, keys, err := KeyCodes(c.chord)
		if c.wantError {
			if err == nil {
				t.Errorf("%q: no error", c.chord)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.chord, err)
			continue
		}
		if len(mods) != c.wantMods {
			t.Errorf("%q: %d modifiers, want %d", c.chord, len(mods), c.wantMods)
		}
		if len(keys) != len(c.wantKeys) {
			t.Errorf("%q: keys %v, want %v", c.chord, keys, c.wantKeys)
			continue
		}
		for i := range keys {
			if keys[i] != c.wantKeys[i] {
				t.Errorf("%q: keys %v, want %v", c.chord, keys, c.wantKeys)
			}
		}
	}
}

func TestButton(t *testing.T) {
	for _, c := range []struct {
		in   string
		want MouseButton
		bad  bool
	}{
		{"", Left, false}, {"left", Left, false}, {"Right", Right, false},
		{"middle", Middle, false}, {"center", Middle, false}, {"sideways", Left, true},
	} {
		got, err := Button(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("Button(%q): no error", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Button(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
}

func TestRect(t *testing.T) {
	a := Rect{X: 10, Y: 10, W: 20, H: 20}
	b := Rect{X: 25, Y: 5, W: 20, H: 20}
	if got := a.Intersect(b); got != (Rect{X: 25, Y: 10, W: 5, H: 15}) {
		t.Errorf("Intersect = %+v", got)
	}
	if got := a.Union(b); got != (Rect{X: 10, Y: 5, W: 35, H: 25}) {
		t.Errorf("Union = %+v", got)
	}
	if !a.Contains(Point{X: 10, Y: 10}) || a.Contains(Point{X: 30, Y: 10}) {
		t.Error("Contains is wrong at the edges")
	}
	if !(Rect{}).Empty() || a.Empty() {
		t.Error("Empty is wrong")
	}
	if got := a.Intersect(Rect{X: 100, Y: 100, W: 5, H: 5}); !got.Empty() {
		t.Errorf("disjoint Intersect = %+v", got)
	}
}
