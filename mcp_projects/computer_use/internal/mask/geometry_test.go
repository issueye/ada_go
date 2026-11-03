//go:build windows

package mask

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"computeruse/internal/win32"
)

func TestSubtract(t *testing.T) {
	area := ui.Rect{X: 0, Y: 0, W: 100, H: 100}
	cases := []struct {
		name  string
		holes []ui.Rect
		want  int // area of the pieces together
	}{
		{"no holes", nil, 10000},
		{"middle", []ui.Rect{{X: 25, Y: 25, W: 50, H: 50}}, 7500},
		{"corner", []ui.Rect{{X: 0, Y: 0, W: 30, H: 30}}, 9100},
		{"all", []ui.Rect{{X: 0, Y: 0, W: 100, H: 100}}, 0},
		{"two", []ui.Rect{{X: 10, Y: 10, W: 20, H: 20}, {X: 60, Y: 60, W: 20, H: 20}}, 10000 - 400 - 400},
		{"outside", []ui.Rect{{X: 200, Y: 200, W: 10, H: 10}}, 10000},
		{"overlap", []ui.Rect{{X: 10, Y: 10, W: 40, H: 40}, {X: 30, Y: 30, W: 40, H: 40}}, 10000 - 1600 - 1600 + 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pieces := subtract(area, c.holes)
			total := 0
			for _, p := range pieces {
				if p.W < 0 || p.H < 0 {
					t.Fatalf("negative piece %+v", p)
				}
				total += int(p.W * p.H)
				for _, h := range c.holes {
					if intersects(p, h) {
						t.Fatalf("piece %+v still overlaps hole %+v", p, h)
					}
				}
			}
			if total != c.want {
				t.Fatalf("pieces cover %d, want %d: %+v", total, c.want, pieces)
			}
		})
	}
}

func TestCoordText(t *testing.T) {
	if got := coordText(win32.Rect{X: 200, Y: 160, W: 720, H: 420}); got != "200,160  720×420" {
		t.Fatalf("coordText = %q", got)
	}
	if got := pointText(win32.Point{X: -12, Y: 34}); got != "-12,34" {
		t.Fatalf("pointText = %q", got)
	}
}

func TestToneName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "accent"}, {"blue", "accent"}, {"green", "green"},
		{"amber", "amber"}, {"red", "red"}, {"white", "white"}, {"accent", "accent"},
	} {
		if got := ToneName(c.in); got != c.want {
			t.Errorf("ToneName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDIPConversion(t *testing.T) {
	w := &window{display: win32.Display{
		Name:   `\\.\DISPLAY2`,
		Bounds: win32.Rect{X: 1920, Y: 0, W: 2560, H: 1440},
		Scale:  2,
	}}
	// A 200,100 point on that display is 100,50 DIPs into the window.
	x, y := w.dipPoint(win32.Point{X: 2020, Y: 100})
	if x != 50 || y != 50 {
		t.Fatalf("dipPoint = %v,%v, want 50,50", x, y)
	}
	r := w.dipRect(win32.Rect{X: 2020, Y: 100, W: 400, H: 200})
	if r.X != 50 || r.Y != 50 || r.W != 200 || r.H != 100 {
		t.Fatalf("dipRect = %+v", r)
	}
}
