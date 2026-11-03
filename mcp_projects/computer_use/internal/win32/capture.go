//go:build windows

package win32

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type rgbQuad struct{ Blue, Green, Red, Reserved byte }

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]rgbQuad
}

// CaptureScreen copies a rectangle of the virtual desktop, in physical
// pixels, into an RGBA image. Layered windows are included, as the eye sees
// them, but windows that opt out of screen capture (WDA_EXCLUDEFROMCAPTURE)
// are not.
func CaptureScreen(r Rect) (*image.RGBA, error) {
	if r.Empty() {
		return nil, errors.New("win32: empty capture rectangle")
	}
	src, _, _ := procGetDC.Call(0)
	if src == 0 {
		return nil, errors.New("win32: GetDC failed")
	}
	defer procReleaseDC.Call(0, src)
	return capture(r.W, r.H, src, func(mem uintptr) bool {
		ok, _, _ := procBitBlt.Call(mem, 0, 0, uintptr(r.W), uintptr(r.H), src, uintptr(r.X), uintptr(r.Y), SRCCOPY|CAPTUREBLT)
		return ok != 0
	})
}

// CaptureScreenFast copies a rectangle of the desktop without CAPTUREBLT,
// which is noticeably faster but leaves out layered windows: a tooltip or a
// drop-down menu of another app may come out empty. The overlay itself is
// excluded from captures either way.
func CaptureScreenFast(r Rect) (*image.RGBA, error) {
	if r.Empty() {
		return nil, errors.New("win32: empty capture rectangle")
	}
	src, _, _ := procGetDC.Call(0)
	if src == 0 {
		return nil, errors.New("win32: GetDC failed")
	}
	defer procReleaseDC.Call(0, src)
	return capture(r.W, r.H, src, func(mem uintptr) bool {
		ok, _, _ := procBitBlt.Call(mem, 0, 0, uintptr(r.W), uintptr(r.H), src, uintptr(r.X), uintptr(r.Y), SRCCOPY)
		return ok != 0
	})
}

// CaptureScaled copies a rectangle of the desktop into an image of w×h
// pixels, letting GDI shrink it on the way: one pass over the screen instead
// of a full-size copy and a resample of the pixels afterwards.
func CaptureScaled(r Rect, w, h int) (*image.RGBA, error) {
	if r.Empty() || w <= 0 || h <= 0 {
		return nil, errors.New("win32: empty capture rectangle")
	}
	src, _, _ := procGetDC.Call(0)
	if src == 0 {
		return nil, errors.New("win32: GetDC failed")
	}
	defer procReleaseDC.Call(0, src)
	return capture(w, h, src, func(mem uintptr) bool {
		procSetStretchBltMode.Call(mem, halftone)
		procSetBrushOrgEx.Call(mem, 0, 0, 0)
		ok, _, _ := procStretchBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), src,
			uintptr(r.X), uintptr(r.Y), uintptr(r.W), uintptr(r.H), SRCCOPY)
		return ok != 0
	})
}

// CaptureWindow copies what a window shows, including the parts another
// window covers or the screen does not show, when the window draws them.
func CaptureWindow(hwnd uintptr) (*image.RGBA, error) {
	r := windowRect(hwnd)
	if r.Empty() {
		return nil, errors.New("win32: the window has no size")
	}
	src, _, _ := procGetDC.Call(hwnd)
	if src == 0 {
		return nil, errors.New("win32: GetDC failed")
	}
	defer procReleaseDC.Call(hwnd, src)
	return capture(r.W, r.H, src, func(mem uintptr) bool {
		ok, _, _ := procPrintWindow.Call(hwnd, mem, PW_RENDERFULLCONTENT)
		return ok != 0
	})
}

// capture draws into a top-down DIB section and reads it as RGBA. The
// pixels of a DIB are BGRA, so the channels are swapped and the alpha
// channel set opaque.
//
// The memory DC and the bitmap are kept from one capture to the next: an
// agent captures the same display over and over, and making them costs a
// few milliseconds each time.
func capture(w, h int, src uintptr, drawInto func(mem uintptr) bool) (*image.RGBA, error) {
	buf := &captureBuf
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if err := buf.fit(w, h); err != nil {
		return nil, err
	}
	old, _, _ := procSelectObject.Call(buf.dc, buf.bmp)
	ok := drawInto(buf.dc)
	procSelectObject.Call(buf.dc, old)
	if !ok {
		return nil, errors.New("win32: the window or the screen did not draw")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// One 32-bit word per pixel: BGRA to RGBA is a swap of the red and blue
	// bytes, and the alpha byte becomes opaque.
	src32 := unsafe.Slice((*uint32)(native(buf.bits)), w*h)
	dst32 := unsafe.Slice((*uint32)(unsafe.Pointer(&img.Pix[0])), w*h)
	for i, v := range src32 {
		dst32[i] = (v & 0xFF00FF00) | ((v & 0xFF) << 16) | ((v >> 16) & 0xFF) | 0xFF000000
	}
	return img, nil
}

// captureBuffer is the memory DC and DIB section reused by captures,
// guarded by its mutex.
type captureBuffer struct {
	mu      sync.Mutex
	dc, bmp uintptr
	bits    uintptr
	w, h    int
}

var captureBuf captureBuffer

// fit makes sure the buffer holds a w×h DIB section.
func (b *captureBuffer) fit(w, h int) error {
	if b.dc == 0 {
		dc, _, _ := procCreateCompatibleDC.Call(0)
		if dc == 0 {
			return errors.New("win32: CreateCompatibleDC failed")
		}
		b.dc = dc
	}
	if b.bmp != 0 && b.w == w && b.h == h {
		return nil
	}
	if b.bmp != 0 {
		procDeleteObject.Call(b.bmp)
		b.bmp, b.bits, b.w, b.h = 0, 0, 0, 0
	}
	bi := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      -int32(h), // top-down
		Planes:      1,
		BitCount:    32,
		Compression: BIRGB,
	}}
	var bits uintptr
	bmp, _, err := procCreateDIBSection.Call(b.dc, uintptr(unsafe.Pointer(&bi)), DIBRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == 0 {
		return fmt.Errorf("win32: CreateDIBSection failed: %v", err)
	}
	b.bmp, b.bits, b.w, b.h = bmp, bits, w, h
	return nil
}

// ClipboardText returns the clipboard's text, "" when it holds none.
func ClipboardText() (string, error) {
	if err := openClipboard(); err != nil {
		return "", err
	}
	defer procCloseClipboard.Call()
	if r, _, _ := procIsClipboardFormatAvailable.Call(CFUNICODETEXT); r == 0 {
		return "", nil
	}
	h, _, _ := procGetClipboardData.Call(CFUNICODETEXT)
	if h == 0 {
		return "", nil
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", errors.New("win32: GlobalLock failed")
	}
	defer procGlobalUnlock.Call(h)
	// Read until the terminating NUL, bounded to keep a corrupt handle from
	// reading for ever.
	base := native(p)
	var units []uint16
	for i := 0; i < 1<<24; i++ {
		u := *(*uint16)(unsafe.Add(base, uintptr(i)*2))
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return syscall.UTF16ToString(units), nil
}

// SetClipboardText replaces the clipboard's text.
func SetClipboardText(text string) error {
	units := syscall.StringToUTF16(text)
	if err := openClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()
	if r, _, _ := procEmptyClipboard.Call(); r != 0 {
		// ok
	} else {
		return errors.New("win32: EmptyClipboard failed")
	}
	size := uintptr(len(units) * 2)
	h, _, err := procGlobalAlloc.Call(GMEM_MOVEABLE, size)
	if h == 0 {
		return fmt.Errorf("win32: GlobalAlloc failed: %v", err)
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("win32: GlobalLock failed")
	}
	copy(unsafe.Slice((*uint16)(native(p)), len(units)), units)
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(CFUNICODETEXT, h); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("win32: SetClipboardData failed")
	}
	// The clipboard owns the block now.
	return nil
}

func openClipboard() error {
	deadline := time.Now().Add(time.Second)
	for {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("win32: the clipboard is held by another program")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
