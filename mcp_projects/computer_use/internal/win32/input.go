//go:build windows

package win32

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"
)

type mouseInput struct {
	DX, DY    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type keyboardInput struct {
	VK, Scan  uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// input is the INPUT structure of SendInput, which is the same size for
// the mouse, the keyboard and the hardware (40 bytes on 64-bit Windows).
type input struct {
	Type uint32
	_    uint32
	Data [32]byte
}

func mouseInputOf(flags uint32, dx, dy int32, data uint32) input {
	var in input
	in.Type = INPUTMOUSE
	*(*mouseInput)(unsafe.Pointer(&in.Data[0])) = mouseInput{DX: dx, DY: dy, MouseData: data, Flags: flags}
	return in
}

func keyInputOf(vk, scan uint16, flags uint32) input {
	var in input
	in.Type = INPUTKEYBOARD
	*(*keyboardInput)(unsafe.Pointer(&in.Data[0])) = keyboardInput{VK: vk, Scan: scan, Flags: flags}
	return in
}

func send(ins []input) error {
	if len(ins) == 0 {
		return nil
	}
	n, _, err := procSendInput.Call(uintptr(len(ins)), uintptr(unsafe.Pointer(&ins[0])), unsafe.Sizeof(input{}))
	if int(n) != len(ins) {
		return fmt.Errorf("win32: SendInput inserted %d of %d events: %v", n, len(ins), err)
	}
	return nil
}

// MoveTo moves the pointer to p with injected mouse input, so that the
// window under it sees the move as a user's, and waits for it to arrive.
func MoveTo(p Point) error {
	v, _ := Virtual()
	if v.W < 2 || v.H < 2 {
		return errors.New("win32: no virtual desktop to move in")
	}
	nx := int32(math.Round(float64(p.X-v.X) * 65535 / float64(v.W-1)))
	ny := int32(math.Round(float64(p.Y-v.Y) * 65535 / float64(v.H-1)))
	if err := send([]input{mouseInputOf(MOUSEEVENTF_MOVE|MOUSEEVENTF_ABSOLUTE|MOUSEEVENTF_VIRTUALDESK, nx, ny, 0)}); err != nil {
		return err
	}
	// The normalized coordinates can miss by a pixel on a large desktop, so
	// the pointer is snapped exactly as soon as it is close: waiting for the
	// input to land perfectly would cost a round trip per move.
	deadline := time.Now().Add(40 * time.Millisecond)
	for {
		cur, err := Cursor()
		if err == nil {
			if cur == p {
				return nil
			}
			if abs(cur.X-p.X) <= 1 && abs(cur.Y-p.Y) <= 1 {
				return SetCursor(p)
			}
		}
		if time.Now().After(deadline) {
			return SetCursor(p)
		}
		time.Sleep(time.Millisecond)
	}
}

// MouseButton is the button a click uses.
type MouseButton int

const (
	Left MouseButton = iota
	Right
	Middle
)

func (b MouseButton) flags(down bool) uint32 {
	switch b {
	case Right:
		if down {
			return MOUSEEVENTF_RIGHTDOWN
		}
		return MOUSEEVENTF_RIGHTUP
	case Middle:
		if down {
			return MOUSEEVENTF_MIDDLEDOWN
		}
		return MOUSEEVENTF_MIDDLEUP
	default:
		if down {
			return MOUSEEVENTF_LEFTDOWN
		}
		return MOUSEEVENTF_LEFTUP
	}
}

// Button parses "left", "right" or "middle".
func Button(name string) (MouseButton, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "left", "l":
		return Left, nil
	case "right", "r":
		return Right, nil
	case "middle", "m", "center":
		return Middle, nil
	}
	return Left, fmt.Errorf("win32: unknown mouse button %q", name)
}

// Click presses and releases b at the pointer, count times, as a user does.
func Click(b MouseButton, count int) error {
	if count < 1 {
		count = 1
	}
	for i := 0; i < count; i++ {
		if err := send([]input{mouseInputOf(b.flags(true), 0, 0, 0)}); err != nil {
			return err
		}
		time.Sleep(12 * time.Millisecond)
		if err := send([]input{mouseInputOf(b.flags(false), 0, 0, 0)}); err != nil {
			return err
		}
		if i < count-1 {
			// Two presses closer than the double-click time count as one
			// double click, which is what a count of 2 means.
			time.Sleep(30 * time.Millisecond)
		}
	}
	return nil
}

// Scroll turns the wheel at p: dy positive notches scroll down (the
// content moves up), dx positive notches scroll right. Windows counts a
// wheel turned away from the user as positive, which scrolls up.
func Scroll(p Point, dx, dy int) error {
	if dx == 0 && dy == 0 {
		return nil
	}
	if err := MoveTo(p); err != nil {
		return err
	}
	var ins []input
	// A wheel turned away from the user has positive mouseData and scrolls
	// up, so a positive dy, which scrolls down, sends negative data.
	for i := 0; i < abs(dy); i++ {
		data := uint32(WHEELDELTA)
		if dy > 0 {
			neg := int32(-WHEELDELTA)
			data = uint32(neg)
		}
		ins = append(ins, mouseInputOf(MOUSEEVENTF_WHEEL, 0, 0, data))
	}
	for i := 0; i < abs(dx); i++ {
		data := uint32(WHEELDELTA)
		if dx < 0 {
			neg := int32(-WHEELDELTA)
			data = uint32(neg)
		}
		ins = append(ins, mouseInputOf(MOUSEEVENTF_HWHEEL, 0, 0, data))
	}
	return send(ins)
}

// Drag presses b at from, moves the pointer to to in steps, and releases
// it, as a user dragging a file or a selection does.
func Drag(from, to Point, b MouseButton, steps int) error {
	if steps < 2 {
		steps = 12
	}
	if err := MoveTo(from); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	if err := send([]input{mouseInputOf(b.flags(true), 0, 0, 0)}); err != nil {
		return err
	}
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		p := Point{
			X: from.X + int(math.Round(float64(to.X-from.X)*t)),
			Y: from.Y + int(math.Round(float64(to.Y-from.Y)*t)),
		}
		if err := MoveTo(p); err != nil {
			return err
		}
		time.Sleep(6 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	return send([]input{mouseInputOf(b.flags(false), 0, 0, 0)})
}

// Type types text with the keyboard, as a user does, through Unicode
// events that reach any layout. A newline is Enter and a tab is Tab.
//
// Without a delay the text goes in batches of a few hundred events: one
// SendInput call for every ~100 characters, which a window reads as fast as
// it can, instead of a call and a wait for each character.
func Type(text string, delay time.Duration) error {
	if delay <= 0 {
		var batch []input
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			err := send(batch)
			batch = batch[:0]
			return err
		}
		for _, r := range text {
			ev := typeInputs(r)
			if len(batch)+len(ev) > 256 {
				if err := flush(); err != nil {
					return err
				}
			}
			batch = append(batch, ev...)
		}
		return flush()
	}
	for _, r := range text {
		if ev := typeInputs(r); len(ev) > 0 {
			if err := send(ev); err != nil {
				return err
			}
		}
		time.Sleep(delay)
	}
	return nil
}

// typeInputs holds the events that type one rune: a key press for Enter and
// Tab, Unicode events for anything else.
func typeInputs(r rune) []input {
	switch r {
	case '\r':
		// The newline after it types the Enter.
		return nil
	case '\n':
		return []input{keyInputOf(0x0D, 0, 0), keyInputOf(0x0D, 0, KEYEVENTF_KEYUP)}
	case '\t':
		return []input{keyInputOf(0x09, 0, 0), keyInputOf(0x09, 0, KEYEVENTF_KEYUP)}
	}
	units := utf16.Encode([]rune{r})
	ins := make([]input, 0, len(units)*2)
	for _, u := range units {
		ins = append(ins, keyInputOf(0, u, KEYEVENTF_UNICODE))
	}
	for i := len(units) - 1; i >= 0; i-- {
		ins = append(ins, keyInputOf(0, units[i], KEYEVENTF_UNICODE|KEYEVENTF_KEYUP))
	}
	return ins
}

var vkNames = map[string]uint16{
	"esc": 0x1B, "escape": 0x1B,
	"enter": 0x0D, "return": 0x0D,
	"tab":       0x09,
	"space":     0x20,
	"backspace": 0x08, "back": 0x08,
	"delete": 0x2E, "del": 0x2E,
	"insert": 0x2D, "ins": 0x2D,
	"home": 0x24, "end": 0x23,
	"pageup": 0x21, "pgup": 0x21, "prior": 0x21,
	"pagedown": 0x22, "pgdn": 0x22, "next": 0x22,
	"up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
	"capslock": 0x14, "caps": 0x14,
	"numlock": 0x90, "scrolllock": 0x91,
	"printscreen": 0x2C, "prtsc": 0x2C, "snapshot": 0x2C,
	"pause": 0x13, "break": 0x13,
	"apps": 0x5D, "menu": 0x5D,
	"minus": 0xBD, "plus": 0xBB, "equal": 0xBB,
	"comma": 0xBC, "period": 0xBE, "dot": 0xBE,
	"slash": 0xBF, "backslash": 0xDC,
	"semicolon": 0xBA, "quote": 0xDE,
	"bracketleft": 0xDB, "bracketright": 0xDD, "grave": 0xC0,
	"num0": 0x60, "num1": 0x61, "num2": 0x62, "num3": 0x63, "num4": 0x64,
	"num5": 0x65, "num6": 0x66, "num7": 0x67, "num8": 0x68, "num9": 0x69,
	"numadd": 0x6B, "numsub": 0x6D, "nummul": 0x6A, "numdiv": 0x6F, "numdecimal": 0x6E,
	"playpause": 0xB3, "nexttrack": 0xB0, "prevtrack": 0xB1,
	"volumemute": 0xAD, "volumedown": 0xAE, "volumeup": 0xAF,
}

var extendedKeys = map[uint16]bool{
	0x24: true, 0x23: true, 0x21: true, 0x22: true,
	0x26: true, 0x28: true, 0x25: true, 0x27: true,
	0x2D: true, 0x2E: true, 0x2C: true, 0x5D: true, 0x5B: true, 0x5C: true,
	0x90: true, 0xA3: true, 0xA5: true, 0x6F: true,
}

// KeyCodes parses a key chord such as "ctrl+shift+t", "alt+tab" or
// "win+r" into the keys to hold and the one to press. A single character
// is that key on the keyboard.
func KeyCodes(chord string) (modifiers []uint16, keys []uint16, err error) {
	parts := strings.Split(chord, "+")
	// "ctrl++" and "ctrl+-" split into an empty part.
	var cleaned []string
	for i, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			if i > 0 && i < len(parts)-1 {
				cleaned = append(cleaned, "+")
			}
			continue
		}
		cleaned = append(cleaned, p)
	}
	if len(cleaned) == 0 {
		return nil, nil, fmt.Errorf("win32: empty key chord")
	}
	for i, p := range cleaned {
		last := i == len(cleaned)-1
		switch p {
		case "ctrl", "control", "ctl":
			modifiers = append(modifiers, 0xA2) // left control
		case "shift":
			modifiers = append(modifiers, 0xA0)
		case "alt", "option":
			modifiers = append(modifiers, 0xA4) // left alt
		case "win", "meta", "cmd", "super":
			modifiers = append(modifiers, 0x5B)
		default:
			vk, ok := keyCode(p)
			if !ok {
				if !last {
					return nil, nil, fmt.Errorf("win32: unknown key %q in %q", p, chord)
				}
				return nil, nil, fmt.Errorf("win32: unknown key %q", p)
			}
			keys = append(keys, vk)
		}
	}
	if len(keys) == 0 {
		return nil, nil, fmt.Errorf("win32: %q holds only modifiers", chord)
	}
	return modifiers, keys, nil
}

func keyCode(name string) (uint16, bool) {
	if vk, ok := vkNames[name]; ok {
		return vk, true
	}
	if len(name) == 1 {
		c := name[0]
		switch {
		case 'a' <= c && c <= 'z':
			return uint16(c - 'a' + 'A'), true
		case '0' <= c && c <= '9':
			return uint16(c), true
		}
		// Punctuation of the US layout, by character.
		if vk := vkForPunctuation(c); vk != 0 {
			return vk, true
		}
	}
	if strings.HasPrefix(name, "f") && len(name) <= 3 {
		var n int
		if _, err := fmt.Sscanf(name, "f%d", &n); err == nil && n >= 1 && n <= 24 {
			return uint16(0x70 + n - 1), true
		}
	}
	return 0, false
}

func vkForPunctuation(c byte) uint16 {
	switch c {
	case '-':
		return 0xBD
	case '=':
		return 0xBB
	case ',':
		return 0xBC
	case '.':
		return 0xBE
	case '/':
		return 0xBF
	case '\\':
		return 0xDC
	case ';':
		return 0xBA
	case '\'':
		return 0xDE
	case '[':
		return 0xDB
	case ']':
		return 0xDD
	case '`':
		return 0xC0
	}
	return 0
}

// KeyCombo presses a key chord: the modifiers go down, the keys go down
// and up in turn, and the modifiers let go in reverse order.
func KeyCombo(chord string) error {
	mods, keys, err := KeyCodes(chord)
	if err != nil {
		return err
	}
	var ins []input
	for _, m := range mods {
		ins = append(ins, keyInputOf(m, 0, 0))
	}
	for _, k := range keys {
		flags := uint32(0)
		if extendedKeys[k] {
			flags |= KEYEVENTF_EXTENDEDKEY
		}
		ins = append(ins, keyInputOf(k, 0, flags))
	}
	for i := len(keys) - 1; i >= 0; i-- {
		flags := uint32(KEYEVENTF_KEYUP)
		if extendedKeys[keys[i]] {
			flags |= KEYEVENTF_EXTENDEDKEY
		}
		ins = append(ins, keyInputOf(keys[i], 0, flags))
	}
	for i := len(mods) - 1; i >= 0; i-- {
		ins = append(ins, keyInputOf(mods[i], 0, KEYEVENTF_KEYUP))
	}
	return send(ins)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
