package nodemods

// 本文件是 node:zlib 的 Go 侧绑定：gzip / zlib(deflate) / raw deflate 的同步编解码。
//
// 与 Node 的偏差（12 §6.12.2 zlib 行）：没有 brotli、没有 zstd、没有流式 createGzip；
// 解压产物夹到 16MB —— 压缩炸弹在宿主侧就被截断，不会先把 Runtime 的堆撑爆。
//
// 字节边界与 crypto 同一套：JS 侧把二进制表示成「每字符 0..255 的字符串」（latin1）。

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

// MaxDecompressBytes 是单次解压产物的默认上限（12 §6.12.2：「解压产物夹到 16MB」）。
const MaxDecompressBytes = 16 << 20

// maxDecompressBytes 是当前生效的上限：测试用小值才能在几毫秒内验证「炸弹被拒」。
var maxDecompressBytes = MaxDecompressBytes

// SetMaxDecompressBytes 覆盖解压上限（测试与产品配置用；<=0 回落默认值）。
func SetMaxDecompressBytes(n int) {
	if n <= 0 {
		n = MaxDecompressBytes
	}
	maxDecompressBytes = n
}

// MaxDecompressLimit 返回当前生效的解压上限。
func MaxDecompressLimit() int { return maxDecompressBytes }

// zlibBinding 返回 node:zlib 的原生绑定。
func zlibBinding(rt *moejs.Runtime) moejs.Value {
	r := rt.Realm()
	obj := r.NewObject()

	compress := rt.Function("zlib.compress", 2, func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		kind := moejs.Arg(args, 0).String()
		// 二进制走 hex 传输：JS 字符串是 UTF-16，直接塞 0x80-0xFF 会被编码层改写。
		data, err := hex.DecodeString(moejs.Arg(args, 1).String())
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_INVALID_ARG_TYPE", "zlib 入参必须是 hex 字节串: "+err.Error())
		}
		out, err := compressBytes(kind, data)
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_ZLIB_COMPRESS_FAILED", err.Error())
		}
		return moejs.String(hex.EncodeToString(out)), nil
	})
	decompress := rt.Function("zlib.decompress", 2, func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		kind := moejs.Arg(args, 0).String()
		data, err := hex.DecodeString(moejs.Arg(args, 1).String())
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_INVALID_ARG_TYPE", "zlib 入参必须是 hex 字节串: "+err.Error())
		}
		out, err := decompressBytes(kind, data)
		if err != nil {
			code := "ERR_ZLIB_DECOMPRESS_FAILED"
			if strings.Contains(err.Error(), "超过") {
				code = "ERR_BUFFER_TOO_LARGE"
			}
			return moejs.Undefined(), throwCode(rr, code, err.Error())
		}
		return moejs.String(hex.EncodeToString(out)), nil
	})

	_ = obj.SetProp(r, r.KeyFromGoString("compress"), compress)
	_ = obj.SetProp(r, r.KeyFromGoString("decompress"), decompress)
	_ = obj.SetProp(r, r.KeyFromGoString("maxOutputBytes"), engine.Int64Value(int64(maxDecompressBytes)))
	return engine.ObjectValue(obj)
}

// compressBytes 按 kind 压缩：gzip | deflate（zlib 包装）| raw（裸 deflate）。
func compressBytes(kind string, data []byte) ([]byte, error) {
	var buf bytes.Buffer
	switch kind {
	case "gzip":
		w := gzip.NewWriter(&buf)
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	case "deflate":
		w := zlib.NewWriter(&buf)
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	case "raw":
		w, err := flate.NewWriter(&buf, flate.DefaultCompression)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("未知压缩格式 %q（只有 gzip/deflate/raw）", kind)
	}
	return buf.Bytes(), nil
}

// decompressBytes 按 kind 解压，并把产物限制在 MaxDecompressBytes。
func decompressBytes(kind string, data []byte) ([]byte, error) {
	var reader io.Reader
	switch kind {
	case "gzip":
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		reader = gr
	case "deflate":
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		reader = zr
	case "raw":
		fr := flate.NewReader(bytes.NewReader(data))
		defer fr.Close()
		reader = fr
	default:
		return nil, fmt.Errorf("未知压缩格式 %q（只有 gzip/deflate/raw）", kind)
	}
	// 多读一个字节用来判断「是否超限」：超限报错而不是截断（09 §9.5 失败方向）。
	out, err := io.ReadAll(io.LimitReader(reader, int64(maxDecompressBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecompressBytes {
		return nil, fmt.Errorf("解压产物超过上限 %d 字节（解压炸弹，12 §6.12.2）", maxDecompressBytes)
	}
	return out, nil
}
