package plugins

// Ed25519 清单签名（09 §9.1、6.9、11 §16 P0）。
//
// 签名覆盖两段：
//  1. 去掉 sig 字段后的清单 JSON（encoding/json 对 map 键排序，形状确定）；
//  2. 插件目录内容摘要：除根 plugin.json 外全部文件按相对路径排序后，
//     以 "rel\x00len\x00data" 形式压进 sha256（与发现层指纹同构，但保留完整 hex）。
//
// 最终签名串是 Ed25519 对 domain + 清单 JSON + "\n" + 目录摘要 的签名，
// 用 base64.StdEncoding 写入 manifest.sig。
//
// 公钥来源（按顺序）：
//   - ADAGO_PLUGIN_PUBKEYS：逗号分隔的 base64 公钥（可带 "ed25519:" 前缀）；
//   - $ADAGO_HOME/plugin-keys.json 或 ~/.adago/plugin-keys.json：
//     {"keys":["<base64>", ...]} 或 ["<base64>", ...]。
//
// 没有配置公钥时，verified 一律按 third-party 处理（失败关闭，不猜）。

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const signatureDomain = "ada-plugin-v1\n"

// SignManifest 用私钥给清单签名。目录摘要不包含 plugin.json；调用方写入 sig 后
// 不应再改目录内容，否则验签会失败。
func SignManifest(m *Manifest, dir string, priv ed25519.PrivateKey) (string, error) {
	if m == nil {
		return "", errors.New("plugins: 清单为空")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return "", errors.New("plugins: 私钥长度非法")
	}
	payload, err := signaturePayload(m, dir)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)), nil
}

// VerifyManifestSignature 用已配置的公钥验签；没有公钥、签名非法或目录被改动都返回错误。
func VerifyManifestSignature(m *Manifest, dir string) error {
	if m == nil {
		return errors.New("plugins: 清单为空")
	}
	keys, err := configuredPublicKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("plugins: 没有配置插件公钥（ADAGO_PLUGIN_PUBKEYS 或 plugin-keys.json）")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(m.Sig))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("plugins: %s 的 sig 不是合法 Ed25519 base64 签名", m.ID)
	}
	payload, err := signaturePayload(m, dir)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if ed25519.Verify(key, payload, sig) {
			return nil
		}
	}
	return fmt.Errorf("plugins: %s 的签名与目录内容不匹配", m.ID)
}

// signaturePayload 拼出被签名的字节：domain + 规范化清单 + 目录摘要。
func signaturePayload(m *Manifest, dir string) ([]byte, error) {
	canonical, err := canonicalManifest(m)
	if err != nil {
		return nil, err
	}
	digest, err := directoryDigest(dir)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, 0, len(signatureDomain)+len(canonical)+1+len(digest))
	payload = append(payload, signatureDomain...)
	payload = append(payload, canonical...)
	payload = append(payload, '\n')
	payload = append(payload, digest...)
	return payload, nil
}

// canonicalManifest 返回去掉 sig 的清单 JSON（map 键由 encoding/json 排序）。
func canonicalManifest(m *Manifest) ([]byte, error) {
	copy := *m
	copy.Sig = ""
	raw, err := json.Marshal(copy)
	if err != nil {
		return nil, fmt.Errorf("plugins: 清单无法序列化: %w", err)
	}
	return raw, nil
}

// directoryDigest 计算除根 plugin.json 外的目录摘要（完整 sha256 hex）。
func directoryDigest(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	type item struct {
		rel  string
		data []byte
	}
	var items []item
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" && path != abs {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "plugin.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		items = append(items, item{rel: rel, data: data})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("plugins: 读取插件目录失败: %w", err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })
	h := sha256.New()
	for _, it := range items {
		fmt.Fprintf(h, "%s\x00%d\x00", it.rel, len(it.data))
		h.Write(it.data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// configuredPublicKeys 读取环境变量或用户级 plugin-keys.json。
func configuredPublicKeys() ([]ed25519.PublicKey, error) {
	if raw := strings.TrimSpace(os.Getenv("ADAGO_PLUGIN_PUBKEYS")); raw != "" {
		return parsePublicKeys(strings.Split(raw, ","))
	}
	path, err := publicKeysPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("plugins: 读取公钥配置失败: %w", err)
	}
	var body struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && len(body.Keys) > 0 {
		return parsePublicKeys(body.Keys)
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("plugins: plugin-keys.json 形状非法（需要 {\"keys\":[...]} 或 [...]）")
	}
	return parsePublicKeys(list)
}

func publicKeysPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("ADAGO_HOME")); dir != "" {
		return filepath.Join(dir, "plugin-keys.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("plugins: 无法定位用户主目录")
	}
	return filepath.Join(home, ".adago", "plugin-keys.json"), nil
}

func parsePublicKeys(raw []string) ([]ed25519.PublicKey, error) {
	out := make([]ed25519.PublicKey, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "ed25519:")
		if s == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("plugins: 公钥 %q 不是合法 Ed25519 base64 公钥", s)
		}
		out = append(out, ed25519.PublicKey(b))
	}
	return out, nil
}
