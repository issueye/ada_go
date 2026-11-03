//go:build windows

// Package mask draws the overlay this program puts over the desktop: a
// click-through layer that dims what the agent does not look at, outlines
// the elements it acts on with their coordinates, and shows what it is
// doing while the user watches.
package mask

import (
	"fmt"
	"time"

	"computeruse/internal/win32"
)

// Region is an outlined area of the desktop, in physical pixels, with the
// coordinates the overlay shows next to it.
type Region struct {
	Rect win32.Rect `json:"rect"`
	// Label is what the agent calls the element, such as "Search field".
	Label string `json:"label,omitempty"`
	// Index numbers the region in the overlay, 0 for none.
	Index int `json:"index,omitempty"`
	// Tone is the color of the outline: "" or "accent", "green", "amber",
	// "red", "white".
	Tone string `json:"tone,omitempty"`
	// Dashed draws the outline dashed, for a region the agent will not
	// act on.
	Dashed bool `json:"dashed,omitempty"`
	// Until is when the region goes away; zero keeps it until cleared.
	Until time.Time `json:"-"`
}

// Marker is a point the overlay marks with a crosshair and its coordinates.
type Marker struct {
	At      win32.Point `json:"at"`
	Label   string      `json:"label,omitempty"`
	Tone    string      `json:"tone,omitempty"`
	Until   time.Time   `json:"-"`
	Arrive  bool        `json:"-"`
	started time.Time
}

// Chip is a line of text at a point of the desktop, with the coordinates
// of that point.
type Chip struct {
	At    win32.Point `json:"at"`
	Text  string      `json:"text"`
	Tone  string      `json:"tone,omitempty"`
	Until time.Time   `json:"-"`
}

// Ripple is the feedback of a click at a point.
type Ripple struct {
	At    win32.Point `json:"at"`
	Start time.Time   `json:"-"`
	Tone  string      `json:"tone,omitempty"`
}

// HUD is the pill the overlay shows at the bottom of each display: whether
// the agent runs, and what it did last.
type HUD struct {
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Busy animates a dot while a command runs, as while typing.
	Busy bool   `json:"busy,omitempty"`
	Tone string `json:"tone,omitempty"`
}

// State is what the overlay shows. Coordinates are physical pixels of the
// virtual desktop, as screenshots and actions use them.
type State struct {
	Visible bool `json:"visible"`
	Paused  bool `json:"paused"`
	// Dim is how much of the desktop the overlay darkens, 0 to 0.85.
	Dim float32 `json:"dim"`
	// Dark leaves the regions at full brightness, and dims the rest. With
	// Dim at 0 and Dark true the screen dims to 0.5 by default.
	Dark bool `json:"dark"`
	// Regions and Markers stand out; chips and ripples are feedback.
	Regions []Region `json:"regions,omitempty"`
	Markers []Marker `json:"markers,omitempty"`
	Chips   []Chip   `json:"chips,omitempty"`
	Ripples []Ripple `json:"-"`
	// Trail is a path the overlay draws, fading as it goes: a drag.
	Trail []win32.Point `json:"trail,omitempty"`
	// Grid draws lines every Grid physical pixels, with their coordinates.
	Grid int `json:"grid,omitempty"`
	// Coords shows the coordinates of the pointer, following it.
	Coords bool `json:"coords,omitempty"`
	HUD    HUD  `json:"hud"`

	// Cursor is where the pointer is, when Coords is on.
	Cursor        Marker    `json:"cursor"`
	CursorOK      bool      `json:"-"`
	CursorDisplay string    `json:"-"`
	Revision      int       `json:"revision"`
	UpdatedAt     time.Time `json:"-"`
}

const (
	// defaultDim is how much the overlay darkens the desktop when it shows
	// regions.
	defaultDim = 0.45
	// rippleLife is how long a click's ring shows.
	rippleLife = 700 * time.Millisecond
	// markerArrive is how long a marker takes to fly in.
	markerArrive = 220 * time.Millisecond
)

// ToneName returns the color a tone names, falling back to the accent.
func ToneName(name string) string {
	switch tone(name) {
	case "green", "amber", "red", "white":
		return tone(name)
	}
	return "accent"
}

// Tone returns the color name a tone stands for, "" for the accent.
func tone(name string) string {
	switch name {
	case "green", "amber", "red", "white", "accent":
		return name
	default:
		return "accent"
	}
}

func (s *State) clone() State {
	out := *s
	out.Regions = append([]Region(nil), s.Regions...)
	out.Markers = append([]Marker(nil), s.Markers...)
	out.Chips = append([]Chip(nil), s.Chips...)
	out.Ripples = append([]Ripple(nil), s.Ripples...)
	out.Trail = append([]win32.Point(nil), s.Trail...)
	return out
}

// coordText is the coordinates of a region, as the overlay writes them:
// position and size.
func coordText(r win32.Rect) string {
	return fmt.Sprintf("%d,%d  %d×%d", r.X, r.Y, r.W, r.H)
}

// pointText is the coordinates of a point.
func pointText(p win32.Point) string { return fmt.Sprintf("%d,%d", p.X, p.Y) }
