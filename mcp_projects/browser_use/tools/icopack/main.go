// Command icopack builds assets/icon.ico from internal/app/icon.png.
//
//	go run ./tools/icopack
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
)

var sizes = []int{256, 128, 64, 48, 32, 16}

type entry struct {
	size int
	data []byte
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	srcPath := filepath.Join(root, "internal", "app", "icon.png")
	src, err := os.Open(srcPath)
	if err != nil {
		fatal(err)
	}
	img, err := png.Decode(src)
	_ = src.Close()
	if err != nil {
		fatal(err)
	}

	var entries []entry
	for _, size := range sizes {
		dst := image.NewRGBA(image.Rect(0, 0, size, size))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Over, nil)
		var b bytes.Buffer
		if err := png.Encode(&b, dst); err != nil {
			fatal(err)
		}
		entries = append(entries, entry{size: size, data: b.Bytes()})
	}

	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, uint16(0)) // reserved
	_ = binary.Write(&out, binary.LittleEndian, uint16(1)) // icon
	_ = binary.Write(&out, binary.LittleEndian, uint16(len(entries)))
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		w := byte(e.size)
		if e.size == 256 {
			w = 0
		}
		out.Write([]byte{w, w, 0, 0})
		_ = binary.Write(&out, binary.LittleEndian, uint16(1))  // planes
		_ = binary.Write(&out, binary.LittleEndian, uint16(32)) // bits per pixel
		_ = binary.Write(&out, binary.LittleEndian, uint32(len(e.data)))
		_ = binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		out.Write(e.data)
	}

	dstDir := filepath.Join(root, "assets")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		fatal(err)
	}
	dst := filepath.Join(dstDir, "icon.ico")
	if err := os.WriteFile(dst, out.Bytes(), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s (%d bytes, %d sizes)\n", dst, out.Len(), len(entries))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "icopack:", err)
	os.Exit(1)
}
