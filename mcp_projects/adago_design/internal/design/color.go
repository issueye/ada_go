package design

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Color 模型的扩展计算库：解析、透明度合成、HSL 转换、WCAG 对比度、色阶派生。

// parseColor 解析颜色为 RGBA（0-1 浮点）。支持 #RGB/#RRGGBB/#RRGGBBAA、rgb()/rgba()。
// 无法解析时返回 ok=false。
func parseColor(s string) (r, g, b, a float64, ok bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch {
	case strings.HasPrefix(s, "#"):
		hex := s[1:]
		switch len(hex) {
		case 3:
			r, g, b = hexNibble(hex[0]), hexNibble(hex[1]), hexNibble(hex[2])
			return r, g, b, 1, true
		case 6:
			rv, err1 := strconv.ParseInt(hex[0:2], 16, 32)
			gv, err2 := strconv.ParseInt(hex[2:4], 16, 32)
			bv, err3 := strconv.ParseInt(hex[4:6], 16, 32)
			if err1 != nil || err2 != nil || err3 != nil {
				return 0, 0, 0, 0, false
			}
			return float64(rv) / 255, float64(gv) / 255, float64(bv) / 255, 1, true
		case 8:
			rv, err1 := strconv.ParseInt(hex[0:2], 16, 32)
			gv, err2 := strconv.ParseInt(hex[2:4], 16, 32)
			bv, err3 := strconv.ParseInt(hex[4:6], 16, 32)
			av, err4 := strconv.ParseInt(hex[6:8], 16, 32)
			if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
				return 0, 0, 0, 0, false
			}
			return float64(rv) / 255, float64(gv) / 255, float64(bv) / 255, float64(av) / 255, true
		}
		return 0, 0, 0, 0, false
	case strings.HasPrefix(s, "rgb("), strings.HasPrefix(s, "rgba("):
		inner := strings.TrimSuffix(strings.TrimPrefix(s, "rgb("), ")")
		inner = strings.TrimSuffix(strings.TrimPrefix(inner, "rgba("), ")")
		parts := strings.Split(inner, ",")
		if len(parts) < 3 {
			return 0, 0, 0, 0, false
		}
		nums := make([]float64, 0, 4)
		for i, p := range parts {
			p = strings.TrimSpace(p)
			if strings.HasSuffix(p, "%") {
				v, err := strconv.ParseFloat(strings.TrimSuffix(p, "%"), 64)
				if err != nil {
					return 0, 0, 0, 0, false
				}
				nums = append(nums, v/100)
			} else {
				v, err := strconv.ParseFloat(p, 64)
				if err != nil {
					return 0, 0, 0, 0, false
				}
				if i < 3 {
					nums = append(nums, v/255)
				} else {
					nums = append(nums, v) // alpha 不做 255 归一
				}
			}
		}
		a := 1.0
		if len(nums) >= 4 {
			a = nums[3]
		}
		return nums[0], nums[1], nums[2], a, true
	}
	return 0, 0, 0, 0, false
}

func hexNibble(c byte) float64 {
	v, _ := strconv.ParseInt(string(c), 16, 32)
	return (float64(v)*16 + float64(v)) / 255
}

// compositeOver 把半透明前景色合成到背景上，返回不透明的 RGB（0-255）。
func compositeOver(fgR, fgG, fgB, fgA, bgR, bgG, bgB float64) (uint8, uint8, uint8) {
	mix := func(f, b float64) uint8 {
		v := f*fgA + b*(1-fgA)
		return uint8(math.Round(clamp01(v) * 255))
	}
	return mix(fgR, bgR), mix(fgG, bgG), mix(fgB, bgB)
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// luminance WCAG 相对亮度（输入 0-255 RGB）。
func luminance(r, g, b uint8) float64 {
	lin := func(v uint8) float64 {
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// ContrastRatio 计算两色的 WCAG 对比度；任一色无法解析返回 ok=false。
// 半透明色按 bg 参数合成（bg 为「#RRGGBB」背景，缺省白底）。
func ContrastRatio(fg, bg string) (float64, bool) {
	fr, fg_, fb, fa, ok1 := parseColor(fg)
	br, bg_, bb, ba, ok2 := parseColor(bg)
	if !ok1 || !ok2 {
		return 0, false
	}
	_ = ba
	// 背景若带透明，先合成到白底
	br8, bg8, bb8 := compositeOver(br, bg_, bb, 1, 1, 1, 1)
	if fa < 1 {
		fr8, fg8, fb8 := compositeOver(fr, fg_, fb, fa, float64(br8)/255, float64(bg8)/255, float64(bb8)/255)
		fr, fg_, fb = float64(fr8)/255, float64(fg8)/255, float64(fb8)/255
	} else {
		fr, fg_, fb = clamp01(fr), clamp01(fg_), clamp01(fb)
	}
	l1 := luminance(uint8(math.Round(fr*255)), uint8(math.Round(fg_*255)), uint8(math.Round(fb*255)))
	l2 := luminance(br8, bg8, bb8)
	hi, lo := math.Max(l1, l2), math.Min(l1, l2)
	return (hi + 0.05) / (lo + 0.05), true
}

// hexToHSL 十六进制色转 HSL（h 0-1, s 0-1, l 0-1）。
func hexToHSL(hex string) (h, s, l float64, ok bool) {
	r, g, b, _, ok := parseColor(hex)
	if !ok {
		return 0, 0, 0, false
	}
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (mx + mn) / 2
	if mx == mn {
		return 0, 0, l, true
	}
	d := mx - mn
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h /= 6
	return h, s, l, true
}

// hslToHex HSL 转十六进制。
func hslToHex(h, s, l float64) string {
	h = h - math.Floor(h)
	var r, g, b float64
	if s == 0 {
		r, g, b = l, l, l
	} else {
		q := l + s - l*s
		if l >= 0.5 {
			q = l + s - l*s
		} else {
			q = l * (1 + s)
		}
		p := 2*l - q
		r = hueToRGB(p, q, h+1.0/3.0)
		g = hueToRGB(p, q, h)
		b = hueToRGB(p, q, h-1.0/3.0)
	}
	return fmt.Sprintf("#%02X%02X%02X", uint8(math.Round(clamp01(r)*255)), uint8(math.Round(clamp01(g)*255)), uint8(math.Round(clamp01(b)*255)))
}

func hueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t += 1
	}
	if t > 1 {
		t -= 1
	}
	switch {
	case t < 1.0/6.0:
		return p + (q-p)*6*t
	case t < 1.0/2.0:
		return q
	case t < 2.0/3.0:
		return p + (q-p)*(2.0/3.0-t)*6
	}
	return p
}

// clampL 把亮度限制进范围。
func clampL(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// brandScale 从品牌色生成 10 阶色板（1 最浅 … 10 最深；索引 6 = 品牌色本身）。
func brandScale(primary string) ([]string, bool) {
	h, s, l, ok := hexToHSL(primary)
	if !ok {
		return nil, false
	}
	l0 := clampL(l, 0.36, 0.62)
	targets := []float64{0.95, 0.88, 0.78, 0.68, 0.60, l0, 0.42, 0.34, 0.26, 0.18}
	// 低饱和品牌色在浅阶保持可用饱和度
	adjS := math.Max(s, 0.45)
	out := make([]string, 10)
	for i, lt := range targets {
		if i == 5 {
			out[i] = strings.ToUpper(primary)
			continue
		}
		si := adjS
		if i < 5 {
			si = adjS * (0.55 + 0.45*float64(i)/5) // 浅阶降饱和
		}
		out[i] = hslToHex(h, si, lt)
	}
	return out, true
}

// mixHex 两色按比例混合（t=0 全 a，t=1 全 b）。
func mixHex(a, b string, t float64) (string, bool) {
	ar, ag, ab, _, ok1 := parseColor(a)
	br, bg, bb, _, ok2 := parseColor(b)
	if !ok1 || !ok2 {
		return "", false
	}
	mix1 := func(x, y float64) int { return int(math.Round((x*(1-t) + y*t) * 255)) }
	return fmt.Sprintf("#%02X%02X%02X", mix1(ar, br), mix1(ag, bg), mix1(ab, bb)), true
}

// BrandPalette 是 brandScale 的导出别名：品牌色 10 阶色板。
func BrandPalette(primary string) ([]string, bool) { return brandScale(primary) }
