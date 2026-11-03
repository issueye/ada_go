//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"computeruse/internal/screen"
	"computeruse/internal/win32"
)

// runShot takes one screenshot without opening a window: it is what a
// script uses to see the desktop.
func runShot(args []string) error {
	out := "screenshot.png"
	if v, ok := flagValue(args, "out"); ok && v != "" {
		out = v
	}
	opts := screen.Options{
		MaxWidth: 1600,
		Format:   "png",
	}
	if v, ok := flagValue(args, "max-width"); ok {
		opts.MaxWidth = atoi(v)
	}
	if v, ok := flagValue(args, "scale"); ok {
		fmt.Sscanf(v, "%f", &opts.Scale)
	}
	if v, ok := flagValue(args, "grid"); ok {
		opts.Grid = atoi(v)
	}
	if v, ok := flagValue(args, "format"); ok {
		opts.Format = v
	}
	if v, ok := flagValue(args, "quality"); ok {
		opts.Quality = atoi(v)
	}
	if v, ok := flagValue(args, "display"); ok {
		i := atoi(v)
		_, displays := win32.Virtual()
		if i < 0 || i >= len(displays) {
			return fmt.Errorf("there is no display %d (the desktop has %d)", i, len(displays))
		}
		opts.Region = displays[i].Bounds
	}
	if _, ok := flagValue(args, "w"); ok || flagValueBool(args, "x") {
		opts.Region = win32.Rect{
			X: atoi(flagOr(args, "x", "0")),
			Y: atoi(flagOr(args, "y", "0")),
			W: atoi(flagOr(args, "w", "0")),
			H: atoi(flagOr(args, "h", "0")),
		}
	}
	opts.Cursor = flagBool(args, "cursor")
	shot, err := screen.Take(opts)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(out); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(out, shot.Data, 0o644); err != nil {
		return err
	}
	virtual, displays := win32.Virtual()
	if flagBool(args, "json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"ok":       true,
			"file":     out,
			"width":    shot.Width,
			"height":   shot.Height,
			"region":   shot.Region,
			"scale":    shot.Scale,
			"format":   shot.Format,
			"virtual":  virtual,
			"displays": displays,
			"mapping":  "screen_x = region.x + image_x/scale; screen_y = region.y + image_y/scale",
		})
	}
	fmt.Printf("%s  %d×%d  region %d,%d %d×%d  scale %g\n",
		out, shot.Width, shot.Height, shot.Region.X, shot.Region.Y, shot.Region.W, shot.Region.H, shot.Scale)
	fmt.Printf("desktop %d×%d at %d,%d, %d display(s): screen = region + image/scale\n",
		virtual.W, virtual.H, virtual.X, virtual.Y, len(displays))
	return nil
}

func flagOr(args []string, name, def string) string {
	if v, ok := flagValue(args, name); ok && v != "" {
		return v
	}
	return def
}

// flagValueBool reports whether a flag with a value was given at all.
func flagValueBool(args []string, name string) bool {
	_, ok := flagValue(args, name)
	return ok
}

func atoi(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}
