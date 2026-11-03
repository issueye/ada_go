//go:build windows

package mcp

// toolList describes the tools an agent lists and calls. Coordinates are
// physical pixels of the virtual desktop, origin at the top-left corner of
// the primary display, as screenshots report them.
func toolList() []map[string]any {
	coord := "Physical pixels of the virtual desktop, origin at the top-left corner of the primary display (negative to the left of it)."

	return []map[string]any{
		{
			"name":        "screen_info",
			"description": "Where the screens are, where the pointer is, which window has the keyboard, and what the mask shows. Call this before a screenshot.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "screenshot",
			"description": "A screenshot of the desktop, as a PNG or JPEG with the coordinate mapping. The mask is left out, so this shows what the agent must reason about. " +
				"Add grid to burn coordinates into the image and read positions off it. " + coord,
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"display":   map[string]any{"type": "integer", "description": "Which display to capture, from screen_info; all of them when omitted."},
					"region":    map[string]any{"type": "string", "description": "\"x,y,w,h\" in physical pixels, to capture a part of the desktop at full detail."},
					"max_width": map[string]any{"type": "integer", "description": "Shrink the image to this width; 1600 is a good default, 0 keeps every pixel."},
					"grid":      map[string]any{"type": "integer", "description": "Burn a grid in every N physical pixels, with the coordinates at its lines."},
					"format":    map[string]any{"type": "string", "enum": []string{"png", "jpeg"}, "description": "PNG (default, sharp text) or JPEG (smaller)."},
					"cursor":    map[string]any{"type": "boolean", "description": "Burn a marker and the coordinates of the pointer in."},
				},
			},
		},
		{
			"name":        "run_script",
			"description": "Run JavaScript with the actions of this program bound in as `cu`: cu.click(x,y,{button,clicks}), cu.move, cu.drag, cu.scroll, cu.type, cu.key, cu.wait, cu.focus, cu.outline, cu.screenshot, cu.state, cu.cursor, cu.windows, cu.mask, cu.clearMask, cu.log, cu.recordStart, cu.recordStop, cu.macroRun, cu.macroList. `args` is the script's global `args`; console.log and cu.log come back as output. Set record to save what the script does as a macro.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"source"},
				"properties": map[string]any{
					"source":     map[string]any{"type": "string", "description": "JavaScript source. The value of the last expression is the result."},
					"name":       map[string]any{"type": "string", "description": "A name for error messages."},
					"timeout_ms": map[string]any{"type": "integer", "description": "Stop the script after this long; 60000 by default."},
					"record":     map[string]any{"type": "string", "description": "Record the actions of the script into a macro of this name."},
					"args":       map[string]any{"type": "object", "description": "Values the script reads as args."},
				},
			},
		},
		{
			"name":        "record_start",
			"description": "Start recording the actions of every caller (HTTP, MCP and scripts) into a macro. Finish with record_stop, which saves it.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string", "description": "The macro's name; a timestamped one when omitted."}},
			},
		},
		{
			"name":        "record_stop",
			"description": "Stop recording and save the macro; returns it with its steps.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "list_macros",
			"description": "The macros this program saved, with their names, step counts and dates.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "run_macro",
			"description": "Replay a macro: its steps go through the same actions, with the mask and the pause switch applying. speed scales the waits (2 is twice as fast).",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]any{
					"name":  map[string]any{"type": "string", "description": "The macro's name, from list_macros."},
					"speed": map[string]any{"type": "number", "description": "How fast the waits play, 1 by default."},
				},
			},
		},
		{
			"name":        "macro_script",
			"description": "Write a macro as JavaScript, to read, edit or save as a script file.",
			"inputSchema": map[string]any{
				"type":       "object",
				"required":   []string{"name"},
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
			},
		},
		{
			"name":        "delete_macro",
			"description": "Delete a macro by name.",
			"inputSchema": map[string]any{
				"type":       "object",
				"required":   []string{"name"},
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
			},
		},
		{
			"name":        "batch",
			"description": "Run several actions in order in one call: click, move, drag, scroll, type, key, focus, outline, wait. One round trip instead of one per action, which is the fastest way to drive a form.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"actions"},
				"properties": map[string]any{
					"actions": map[string]any{
						"type":        "array",
						"description": "The actions, each as the action tools take them, with a type such as click or type.",
						"items": map[string]any{
							"type":     "object",
							"required": []string{"type"},
							"properties": map[string]any{
								"type":     map[string]any{"type": "string", "enum": []string{"click", "move", "drag", "scroll", "type", "key", "focus", "outline", "wait"}},
								"x":        map[string]any{"type": "integer"},
								"y":        map[string]any{"type": "integer"},
								"x2":       map[string]any{"type": "integer"},
								"y2":       map[string]any{"type": "integer"},
								"button":   map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}},
								"clicks":   map[string]any{"type": "integer"},
								"dx":       map[string]any{"type": "integer"},
								"dy":       map[string]any{"type": "integer"},
								"text":     map[string]any{"type": "string"},
								"keys":     map[string]any{"type": "string"},
								"repeat":   map[string]any{"type": "integer"},
								"handle":   map[string]any{"type": "integer"},
								"title":    map[string]any{"type": "string"},
								"ms":       map[string]any{"type": "integer"},
								"delay_ms": map[string]any{"type": "integer"},
							},
						},
					},
				},
			},
		},
		{
			"name":        "click",
			"description": "Move the pointer to (x, y) and press a mouse button there, as a user does. The pointer is moved first, so the click lands on whatever is at that point.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"x", "y"},
				"properties": map[string]any{
					"x":      map[string]any{"type": "integer", "description": "X in physical pixels."},
					"y":      map[string]any{"type": "integer", "description": "Y in physical pixels."},
					"button": map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}, "description": "Left when omitted."},
					"clicks": map[string]any{"type": "integer", "description": "1 (default) or 2 for a double click."},
				},
			},
		},
		{
			"name":        "move",
			"description": "Move the pointer to (x, y) without pressing a button, optionally in steps that a window sees as a drag.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"x", "y"},
				"properties": map[string]any{
					"x":     map[string]any{"type": "integer"},
					"y":     map[string]any{"type": "integer"},
					"steps": map[string]any{"type": "integer", "description": "How many moves to make the way; 1 jumps."},
				},
			},
		},
		{
			"name":        "drag",
			"description": "Press a button at (x, y), move to (x2, y2), and release: selects text, moves a window, drags a file.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"x", "y", "x2", "y2"},
				"properties": map[string]any{
					"x":      map[string]any{"type": "integer"},
					"y":      map[string]any{"type": "integer"},
					"x2":     map[string]any{"type": "integer"},
					"y2":     map[string]any{"type": "integer"},
					"button": map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}},
				},
			},
		},
		{
			"name":        "scroll",
			"description": "Turn the wheel at (x, y): dy positive notches scroll down, dx positive notches scroll right. Each notch is 3 lines by default on Windows.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"x", "y"},
				"properties": map[string]any{
					"x":  map[string]any{"type": "integer"},
					"y":  map[string]any{"type": "integer"},
					"dx": map[string]any{"type": "integer", "description": "Notches to the right."},
					"dy": map[string]any{"type": "integer", "description": "Notches down."},
				},
			},
		},
		{
			"name":        "type",
			"description": "Type text at the keyboard focus, through Unicode events, so any layout and any language work. A newline presses Enter, a tab Tab. Focus the window first (focus_window or click).",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"text"},
				"properties": map[string]any{
					"text":     map[string]any{"type": "string", "description": "The text to type."},
					"delay_ms": map[string]any{"type": "integer", "description": "Wait after each character; 8 ms by default."},
				},
			},
		},
		{
			"name":        "key",
			"description": "Press a key chord, as \"enter\", \"tab\", \"esc\", \"ctrl+s\", \"ctrl+shift+t\", \"alt+tab\", \"win+r\".",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"keys"},
				"properties": map[string]any{
					"keys":   map[string]any{"type": "string", "description": "The chord: modifiers joined with +, then the key."},
					"repeat": map[string]any{"type": "integer", "description": "How many times to press it."},
				},
			},
		},
		{
			"name":        "list_windows",
			"description": "The visible windows of the desktop, front to back, with their titles, processes, handles and rectangles. Use it to find what to focus or click.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "focus_window",
			"description": "Bring a window to the front and give it the keyboard, by its handle (from list_windows) or a part of its title. Restores it if minimized.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"handle": map[string]any{"type": "integer", "description": "The window's handle, as list_windows reports it."},
					"title":  map[string]any{"type": "string", "description": "Part of the window's title; the first match wins."},
				},
			},
		},
		{
			"name":        "outline_window",
			"description": "Draw a window in the mask with its coordinates and size, for the user to see which element the agent works on. Without a handle or title it outlines the window that has the keyboard.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"handle": map[string]any{"type": "integer", "description": "The window's handle, from list_windows."},
					"title":  map[string]any{"type": "string", "description": "Part of the window's title."},
					"append": map[string]any{"type": "boolean", "description": "Keep the regions already outlined."},
				},
			},
		},
		{
			"name":        "clipboard",
			"description": "Read the clipboard's text, or replace it. Faster and more exact than typing for large text.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"op"},
				"properties": map[string]any{
					"op":   map[string]any{"type": "string", "enum": []string{"get", "set"}},
					"text": map[string]any{"type": "string", "description": "The text to put on the clipboard, for op set."},
				},
			},
		},
		{
			"name": "mask",
			"description": "Draw the mask over the desktop: dim everything except the regions the agent acts on, outline them with their coordinates and sizes, point at a place with a crosshair, " +
				"write a chip at a point, or turn on the ruler. The user sees it; screenshots do not. Coordinates in physical pixels.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"regions": map[string]any{
						"type":        "array",
						"description": "Areas to outline, each with its coordinates written beside it.",
						"items": map[string]any{
							"type":     "object",
							"required": []string{"rect"},
							"properties": map[string]any{
								"rect":   map[string]any{"type": "object", "description": "x, y, w, h in physical pixels.", "required": []string{"x", "y", "w", "h"}, "properties": map[string]any{"x": map[string]any{"type": "integer"}, "y": map[string]any{"type": "integer"}, "w": map[string]any{"type": "integer"}, "h": map[string]any{"type": "integer"}}},
								"label":  map[string]any{"type": "string", "description": "What the element is, such as \"Search field\"."},
								"index":  map[string]any{"type": "integer", "description": "A number shown with the region."},
								"tone":   map[string]any{"type": "string", "enum": []string{"accent", "green", "amber", "red", "white"}},
								"dashed": map[string]any{"type": "boolean", "description": "Dashed, for something the agent will not act on."},
							},
						},
					},
					"markers": map[string]any{
						"type":        "array",
						"description": "Places to point at, each with a crosshair and its coordinates.",
						"items": map[string]any{
							"type":     "object",
							"required": []string{"at"},
							"properties": map[string]any{
								"at":    map[string]any{"type": "object", "required": []string{"x", "y"}, "properties": map[string]any{"x": map[string]any{"type": "integer"}, "y": map[string]any{"type": "integer"}}},
								"label": map[string]any{"type": "string"},
								"tone":  map[string]any{"type": "string", "enum": []string{"accent", "green", "amber", "red", "white"}},
							},
						},
					},
					"chips": map[string]any{
						"type":        "array",
						"description": "Lines of text at points, each with that point's coordinates.",
						"items": map[string]any{
							"type":     "object",
							"required": []string{"at", "text"},
							"properties": map[string]any{
								"at":   map[string]any{"type": "object", "required": []string{"x", "y"}, "properties": map[string]any{"x": map[string]any{"type": "integer"}, "y": map[string]any{"type": "integer"}}},
								"text": map[string]any{"type": "string"},
								"tone": map[string]any{"type": "string", "enum": []string{"accent", "green", "amber", "red", "white"}},
							},
						},
					},
					"dim":     map[string]any{"type": "number", "description": "How much the mask darkens the rest, 0 to 0.85. 0.45 when omitted with regions."},
					"dark":    map[string]any{"type": "boolean", "description": "Dim everything but the regions (true by default with regions)."},
					"grid":    map[string]any{"type": "integer", "description": "Draw a ruler every N physical pixels, with the coordinates."},
					"coords":  map[string]any{"type": "boolean", "description": "Show the pointer's coordinates beside it (on by default)."},
					"visible": map[string]any{"type": "boolean", "description": "Hide or show the whole mask."},
					"append":  map[string]any{"type": "boolean", "description": "Add the regions to those shown instead of replacing them."},
					"clear":   map[string]any{"type": "boolean", "description": "Remove everything before adding this request."},
					"ttl_ms":  map[string]any{"type": "integer", "description": "How long the regions and markers stay, in milliseconds; 0 keeps them."},
					"hud":     map[string]any{"type": "object", "properties": map[string]any{"detail": map[string]any{"type": "string"}, "busy": map[string]any{"type": "boolean"}}},
				},
			},
		},
		{
			"name":        "mask_preview",
			"description": "Render the mask as an image, to check what the user sees: the dimming, the outlines and their coordinates. The screen capture leaves the mask out.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "clear_mask",
			"description": "Remove every region, marker, chip and the dimming from the mask.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "pause",
			"description": "Hold the agent: every action that injects input fails until resume. The user can do the same with Ctrl+Alt+Shift+P or the tray icon.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "resume",
			"description": "Release the agent after a pause.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "wait",
			"description": "Wait before the next action, for a page to load or an animation to end.",
			"inputSchema": map[string]any{
				"type":       "object",
				"required":   []string{"ms"},
				"properties": map[string]any{"ms": map[string]any{"type": "integer", "description": "Milliseconds to wait, up to 30000."}},
			},
		},
	}
}
