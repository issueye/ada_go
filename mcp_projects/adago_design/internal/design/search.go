package design

import (
	"fmt"
	"sort"
	"strings"
)

// SearchHit 是一条跨体系搜索结果。
type SearchHit struct {
	Kind    string `json:"kind"` // system | component | token
	System  string `json:"system,omitempty"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Snippet string `json:"snippet,omitempty"`
}

// Search 跨体系搜索：体系元信息、组件、Token 名称（颜色/间距/圆角/字型档位）。
// 返回按 kind + id 排序的结果（最多 40 条）。
func Search(query string) []SearchHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	hits := make([]SearchHit, 0, 16)
	add := func(h SearchHit) {
		if len(hits) < 40 {
			hits = append(hits, h)
		}
	}
	for _, sys := range Systems() {
		matchFields := [][2]string{
			{"system", sys.ID}, {"system", sys.Name}, {"system", sys.Vendor},
			{"system", sys.Family}, {"system", sys.Style.Summary},
		}
		for _, kw := range sys.Style.Keywords {
			matchFields = append(matchFields, [2]string{"system", kw})
		}
		for _, f := range matchFields {
			if strings.Contains(strings.ToLower(f[1]), q) {
				add(SearchHit{Kind: "system", System: sys.ID, ID: sys.ID,
					Title: sys.Name, Snippet: firstSnip(f[1], sys.Style.Summary)})
				break
			}
		}
		// Token 名称匹配
		tok, _ := sys.TokensFor(ThemeLight)
		for _, name := range sortedTokenNames(tok.Colors) {
			if strings.Contains(name, q) {
				add(SearchHit{Kind: "token", System: sys.ID, ID: "color/" + name,
					Title: sys.Name + " · " + name, Snippet: tok.Colors[name].Value})
			}
		}
		for _, name := range sortedTokenInts(tok.Spacing) {
			if strings.Contains(name, q) {
				add(SearchHit{Kind: "token", System: sys.ID, ID: "spacing/" + name,
					Title: sys.Name + " · spacing " + name, Snippet: fmt.Sprint(tok.Spacing[name]) + "px"})
			}
		}
		for _, name := range sortedTokenInts(tok.Radius) {
			if strings.Contains(name, q) {
				add(SearchHit{Kind: "token", System: sys.ID, ID: "radius/" + name,
					Title: sys.Name + " · radius " + name, Snippet: fmt.Sprint(tok.Radius[name]) + "px"})
			}
		}
	}
	for _, comp := range Components(nil) {
		texts := [][2]string{
			{"component", comp.ID}, {"component", comp.Name},
			{"component", comp.Category}, {"component", comp.Description},
		}
		for _, f := range texts {
			if strings.Contains(strings.ToLower(f[1]), q) {
				add(SearchHit{Kind: "component", ID: comp.ID, Title: comp.Name,
					Snippet: firstSnip(f[1], comp.Description)})
				break
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		ri, rj := rankOf(hits[i]), rankOf(hits[j])
		if ri != rj {
			return ri < rj
		}
		return hits[i].ID < hits[j].ID
	})
	return hits
}

// rankOf 结果排序权重：component < system < token。
func rankOf(h SearchHit) int {
	switch h.Kind {
	case "component":
		return 0
	case "system":
		return 1
	default:
		return 2
	}
}

func firstSnip(current, fallback string) string {
	if len(current) > 80 {
		return current[:80]
	}
	if len(fallback) > 80 {
		return fallback[:80]
	}
	if current != "" {
		return current
	}
	return fallback
}

func sortedTokenNames(m map[string]Color) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedTokenInts(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
