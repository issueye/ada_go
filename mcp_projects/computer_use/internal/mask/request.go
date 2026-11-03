//go:build windows

package mask

import (
	"fmt"
	"time"
)

// Request asks the overlay to show something: the body of POST /v1/mask and
// of cu.mask in scripts. Fields left out keep what the overlay shows.
type Request struct {
	Visible *bool    `json:"visible,omitempty"`
	Dim     *float32 `json:"dim,omitempty"`
	Dark    *bool    `json:"dark,omitempty"`
	Grid    *int     `json:"grid,omitempty"`
	Coords  *bool    `json:"coords,omitempty"`
	Clear   bool     `json:"clear,omitempty"`
	Append  bool     `json:"append,omitempty"`

	Regions []Region `json:"regions,omitempty"`
	Markers []Marker `json:"markers,omitempty"`
	Chips   []Chip   `json:"chips,omitempty"`
	HUD     *HUD     `json:"hud,omitempty"`

	// TTLMS is how long the regions, markers and chips of this request stay;
	// 0 keeps them until they are cleared.
	TTLMS int `json:"ttl_ms,omitempty"`
}

// Apply shows what the request asks for.
func (m *Mask) Apply(req Request) error {
	if m == nil || m.Disabled() {
		return fmt.Errorf("the overlay is off (started with --no-mask)")
	}
	if req.Clear {
		m.Clear()
	}
	until := time.Now().Add(time.Duration(max(req.TTLMS, 0)) * time.Millisecond)
	if req.TTLMS <= 0 {
		until = time.Time{}
	}
	for i := range req.Regions {
		req.Regions[i].Tone = ToneName(req.Regions[i].Tone)
		if req.Regions[i].Until.IsZero() {
			req.Regions[i].Until = until
		}
		if req.Regions[i].Rect.Empty() {
			return fmt.Errorf("region %d has an empty rect", i)
		}
	}
	for i := range req.Markers {
		req.Markers[i].Tone = ToneName(req.Markers[i].Tone)
		if req.Markers[i].Until.IsZero() {
			req.Markers[i].Until = until
		}
	}
	for i := range req.Chips {
		if req.Chips[i].Until.IsZero() {
			req.Chips[i].Until = until
		}
	}

	opts := Options{}
	if st := m.Snapshot(); true {
		opts = Options{Dim: st.Dim, Dark: st.Dark, Grid: st.Grid, Coords: st.Coords}
	}
	if req.Dim != nil {
		opts.Dim = *req.Dim
	}
	if req.Dark != nil {
		opts.Dark = *req.Dark
	}
	if req.Grid != nil {
		opts.Grid = *req.Grid
	}
	if req.Coords != nil {
		opts.Coords = *req.Coords
	}
	if req.Visible != nil {
		m.SetVisible(*req.Visible)
	}
	m.SetOptions(opts)
	if req.Regions != nil {
		if req.Append {
			m.AddRegions(req.Regions...)
		} else {
			m.ShowRegions(req.Regions)
		}
	}
	for _, mk := range req.Markers {
		m.Mark(mk)
	}
	for _, c := range req.Chips {
		m.ChipAt(c)
	}
	if req.HUD != nil {
		m.Update(func(st *State) { st.HUD = *req.HUD })
	}
	return nil
}
