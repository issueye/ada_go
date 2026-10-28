package nodemods

// 本文件是 node:crypto 的 Go 侧绑定：只做「必须落在标准库上」的四件事——
// 摘要/HMAC、PBKDF2/HKDF、随机数、常量时间比较。对象形状（createHash 的链式
// update/digest、subtle 的 Promise 包装）在 src/crypto.js 里，因为那是 JS 语义。
//
// 字节边界：JS 侧把二进制统一成「每字符 0..255 的字符串」（latin1）再传进来，
// Go 侧逐字符取低 8 位，避免 UTF-16/UTF-8 的二次编码歧义。

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"hash"
	"math/big"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

// Binding 返回某模块需要的原生绑定（crypto 的摘要/随机数、zlib 的编解码）。
func Binding(rt *moejs.Runtime, name string) (moejs.Value, bool) {
	switch strings.TrimPrefix(name, "node:") {
	case "crypto":
		return cryptoBinding(rt), true
	case "zlib":
		return zlibBinding(rt), true
	}
	return moejs.Value{}, false
}

// cryptoBinding 返回 node:crypto 的原生绑定。
func cryptoBinding(rt *moejs.Runtime) moejs.Value {
	r := rt.Realm()
	obj := r.NewObject()
	put := func(key string, fn engine.NativeFunc) {
		_ = obj.SetProp(r, r.KeyFromGoString(key), rt.Function("crypto."+key, 3, fn))
	}
	put("digestHex", func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		algo := moejs.Arg(args, 0).String()
		data, err := binBytes(moejs.Arg(args, 1))
		if err != nil {
			return moejs.Undefined(), err
		}
		factory, err := hashFactory(algo)
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_INVALID_DIGEST", err.Error())
		}
		h := factory()
		_, _ = h.Write(data)
		return moejs.String(hex.EncodeToString(h.Sum(nil))), nil
	})
	put("hmacHex", func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		algo := moejs.Arg(args, 0).String()
		key, err := binBytes(moejs.Arg(args, 1))
		if err != nil {
			return moejs.Undefined(), err
		}
		data, err := binBytes(moejs.Arg(args, 2))
		if err != nil {
			return moejs.Undefined(), err
		}
		factory, err := hashFactory(algo)
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_INVALID_DIGEST", err.Error())
		}
		mac := hmac.New(factory, key)
		_, _ = mac.Write(data)
		return moejs.String(hex.EncodeToString(mac.Sum(nil))), nil
	})
	put("pbkdf2Hex", func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		digest := moejs.Arg(args, 0).String()
		password, err := binBytes(moejs.Arg(args, 1))
		if err != nil {
			return moejs.Undefined(), err
		}
		salt, err := binBytes(moejs.Arg(args, 2))
		if err != nil {
			return moejs.Undefined(), err
		}
		iter, ok := intArg(args, 3)
		if !ok || iter <= 0 {
			return moejs.Undefined(), throwCode(rr, "ERR_OUT_OF_RANGE", "pbkdf2 的 iterations 必须是正整数")
		}
		keyLen, ok := intArg(args, 4)
		if !ok || keyLen <= 0 {
			return moejs.Undefined(), throwCode(rr, "ERR_OUT_OF_RANGE", "pbkdf2 的 keylen 必须是正整数")
		}
		if iter > 1_000_000 {
			return moejs.Undefined(), throwCode(rr, "ERR_OUT_OF_RANGE", "pbkdf2 的 iterations 超过宿主上限 1000000")
		}
		factory, err := hashFactory(digest)
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_INVALID_DIGEST", err.Error())
		}
		out, err := pbkdf2.Key(factory, string(password), salt, iter, keyLen)
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_INVALID_DIGEST", err.Error())
		}
		return moejs.String(hex.EncodeToString(out)), nil
	})
	put("hkdfHex", func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		digest := moejs.Arg(args, 0).String()
		ikm, err := binBytes(moejs.Arg(args, 1))
		if err != nil {
			return moejs.Undefined(), err
		}
		salt, err := binBytes(moejs.Arg(args, 2))
		if err != nil {
			return moejs.Undefined(), err
		}
		info, err := binBytes(moejs.Arg(args, 3))
		if err != nil {
			return moejs.Undefined(), err
		}
		length, ok := intArg(args, 4)
		if !ok || length <= 0 || length > 1<<16 {
			return moejs.Undefined(), throwCode(rr, "ERR_OUT_OF_RANGE", "hkdf 的 length 必须在 1..65536")
		}
		factory, err := hashFactory(digest)
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_INVALID_DIGEST", err.Error())
		}
		out, err := hkdfKey(factory, ikm, salt, string(info), length)
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_INVALID_DIGEST", err.Error())
		}
		return moejs.String(hex.EncodeToString(out)), nil
	})
	put("randomBytesHex", func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		n, ok := intArg(args, 0)
		if !ok || n < 0 {
			return moejs.Undefined(), throwCode(rr, "ERR_OUT_OF_RANGE", "randomBytes 的长度必须是非负整数")
		}
		if n > 1<<20 {
			return moejs.Undefined(), throwCode(rr, "ERR_OUT_OF_RANGE", "randomBytes 单次上限 1048576 字节")
		}
		buf := make([]byte, n)
		if _, err := rand.Read(buf); err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_OPERATION_FAILED", err.Error())
		}
		return moejs.String(hex.EncodeToString(buf)), nil
	})
	put("randomInt", func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		min, okMin := intArg(args, 0)
		if !okMin {
			return moejs.Undefined(), throwCode(rr, "ERR_INVALID_ARG_TYPE", "randomInt 的参数必须是数字")
		}
		max, okMax := intArg(args, 1)
		if !okMax {
			// randomInt(max)：等价于 randomInt(0, max)。
			max, min = min, 0
		}
		if min >= max {
			return moejs.Undefined(), throwCode(rr, "ERR_OUT_OF_RANGE", fmt.Sprintf("randomInt 的区间为空：[%d, %d)", min, max))
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min)))
		if err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_OPERATION_FAILED", err.Error())
		}
		return engine.Int64Value(int64(min) + n.Int64()), nil
	})
	put("randomUUID", func(rr *engine.Realm, _ engine.Value, _ []engine.Value) (moejs.Value, error) {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_OPERATION_FAILED", err.Error())
		}
		b[6] = (b[6] & 0x0f) | 0x40 // version 4
		b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
		return moejs.String(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])), nil
	})
	put("timingSafeEqual", func(rr *engine.Realm, _ engine.Value, args []engine.Value) (moejs.Value, error) {
		a, err := binBytes(moejs.Arg(args, 0))
		if err != nil {
			return moejs.Undefined(), err
		}
		b, err := binBytes(moejs.Arg(args, 1))
		if err != nil {
			return moejs.Undefined(), err
		}
		if len(a) != len(b) {
			return moejs.Undefined(), throwCode(rr, "ERR_CRYPTO_TIMING_SAFE_EQUAL_LENGTH",
				fmt.Sprintf("Input buffers must have the same byte length: %d != %d", len(a), len(b)))
		}
		return engine.Bool(subtle.ConstantTimeCompare(a, b) == 1), nil
	})
	return engine.ObjectValue(obj)
}

// hashFactory 返回算法名对应的构造函数。
//
// hmac/pbkdf2/hkdf 都要求「每次调用产生一个新实例」（Go 的 hmac.New 会检查
// 构造函数的唯一性），所以这里给的是工厂而不是单个 hash.Hash。
func hashFactory(algo string) (func() hash.Hash, error) {
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case "md5":
		return md5.New, nil
	case "sha1", "sha-1":
		return sha1.New, nil
	case "sha256", "sha-256":
		return sha256.New, nil
	case "sha512", "sha-512":
		return sha512.New, nil
	case "sha3-256":
		return func() hash.Hash { return sha3.New256() }, nil
	default:
		return nil, fmt.Errorf("%q is not a supported algorithm（12 §6.12.2：只装 md5/sha1/sha256/sha512/sha3-256）", algo)
	}
}

// binBytes 把 JS 的 latin1 二进制字符串逐字符取低 8 位还原成字节。
func binBytes(v moejs.Value) ([]byte, error) {
	if !v.IsString() {
		return nil, &codedError{Code: "ERR_INVALID_ARG_TYPE", Message: "参数必须是二进制字符串（Buffer/Uint8Array 先转 latin1）"}
	}
	s := v.String()
	out := make([]byte, 0, len(s))
	for _, ch := range s {
		if ch > 0xff {
			return nil, &codedError{Code: "ERR_INVALID_ARG_TYPE", Message: "二进制字符串里出现了 >0xFF 的码位（先按 UTF-8 编码）"}
		}
		out = append(out, byte(ch))
	}
	return out, nil
}

func intArg(args []moejs.Value, i int) (int, bool) {
	v := moejs.Arg(args, i)
	if !v.IsNumber() {
		return 0, false
	}
	f := v.AsNumber()
	if f != float64(int(f)) {
		return 0, false
	}
	return int(f), true
}

// throwCode 造一个带 code 的 Error 并抛出（JS 侧按 err.code 分支）。
func throwCode(r *engine.Realm, code, message string) error {
	v := r.NewErrorValue(engine.KindError, engine.FromGoString(message))
	if o := v.AsObject(); o != nil {
		_ = o.SetProp(r, r.KeyFromGoString("code"), moejs.String(code))
	}
	return r.Throw(v)
}

// hkdfKey 走标准库 crypto/hkdf（RFC 5869）。
func hkdfKey(h func() hash.Hash, secret, salt []byte, info string, length int) ([]byte, error) {
	return hkdf.Key(h, secret, salt, info, length)
}

// codedError 让 binBytes 的错误能被上层识别（当前只用于内部错误文本）。
type codedError struct {
	Code    string
	Message string
}

func (e *codedError) Error() string { return e.Code + ": " + e.Message }
