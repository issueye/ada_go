package browser

import (
	"strings"
	"time"

	"github.com/egoist/mygo"
)

// CookieInfo is a JSON-friendly cookie.
type CookieInfo struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path,omitempty"`
	Expires  string `json:"expires,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	HTTPOnly bool   `json:"http_only,omitempty"`
	SameSite string `json:"same_site,omitempty"`
}

// CookieOptions selects a cookies action.
type CookieOptions struct {
	Action   string
	URL      string
	Name     string
	Value    string
	Domain   string
	Path     string
	Secure   bool
	HTTPOnly bool
	SameSite string
	Expires  string
	Cookies  []CookieInfo
}

// CookiesResult is returned by Manager.Cookies.
type CookiesResult struct {
	Cookies  []CookieInfo `json:"cookies,omitempty"`
	Count    int          `json:"count"`
	Changed  int          `json:"changed,omitempty"`
	Imported int          `json:"imported,omitempty"`
	Action   string       `json:"action"`
}

// Cookies lists or changes the agent session's cookies.
func (m *Manager) Cookies(opts CookieOptions) (*CookiesResult, error) {
	if m.session == nil {
		return nil, &Error{Code: CodeInternal, Message: "no browser session"}
	}
	mod := m.session.Cookies
	action := opts.Action
	if action == "" {
		action = "list"
	}
	switch action {
	case "list", "export":
		list, err := mod.List(mygo.CookieFilter{URL: opts.URL, Name: opts.Name, Domain: opts.Domain, Path: opts.Path})
		if err != nil {
			return nil, cookieErr(err)
		}
		return &CookiesResult{Cookies: cookieInfos(list), Count: len(list), Action: action}, nil
	case "set":
		if opts.Name == "" || opts.Domain == "" {
			return nil, &Error{Code: CodeBadRequest, Message: "name and domain are required"}
		}
		c, err := toCookie(opts)
		if err != nil {
			return nil, err
		}
		if err := mod.Set(c); err != nil {
			return nil, cookieErr(err)
		}
		return &CookiesResult{Action: action, Changed: 1}, nil
	case "delete":
		if opts.Name == "" || opts.Domain == "" {
			return nil, &Error{Code: CodeBadRequest, Message: "name and domain are required"}
		}
		if err := mod.Delete(mygo.Cookie{Name: opts.Name, Domain: opts.Domain, Path: opts.Path}); err != nil {
			return nil, cookieErr(err)
		}
		return &CookiesResult{Action: action, Changed: 1}, nil
	case "clear":
		if opts.URL == "" && opts.Name == "" && opts.Domain == "" && opts.Path == "" {
			if err := mod.Clear(); err != nil {
				return nil, cookieErr(err)
			}
			return &CookiesResult{Action: action, Changed: 1}, nil
		}
		list, err := mod.List(mygo.CookieFilter{URL: opts.URL, Name: opts.Name, Domain: opts.Domain, Path: opts.Path})
		if err != nil {
			return nil, cookieErr(err)
		}
		for _, c := range list {
			if err := mod.Delete(c); err != nil {
				return nil, cookieErr(err)
			}
		}
		return &CookiesResult{Action: action, Changed: len(list)}, nil
	case "import":
		if len(opts.Cookies) == 0 {
			return nil, &Error{Code: CodeBadRequest, Message: "cookies is required"}
		}
		for _, ci := range opts.Cookies {
			c, err := toCookie(CookieOptions{
				Name: ci.Name, Value: ci.Value, Domain: ci.Domain, Path: ci.Path,
				Secure: ci.Secure, HTTPOnly: ci.HTTPOnly, SameSite: ci.SameSite, Expires: ci.Expires,
			})
			if err != nil {
				return nil, err
			}
			if err := mod.Set(c); err != nil {
				return nil, cookieErr(err)
			}
		}
		return &CookiesResult{Action: action, Imported: len(opts.Cookies)}, nil
	default:
		return nil, &Error{Code: CodeBadRequest, Message: "unknown cookies action: " + action}
	}
}

func toCookie(o CookieOptions) (mygo.Cookie, error) {
	path := o.Path
	if path == "" {
		path = "/"
	}
	c := mygo.Cookie{
		Name: o.Name, Value: o.Value, Domain: strings.ToLower(o.Domain), Path: path,
		Secure: o.Secure, HTTPOnly: o.HTTPOnly,
	}
	switch strings.ToLower(o.SameSite) {
	case "", "lax":
		c.SameSite = mygo.CookieSameSiteLax
	case "strict":
		c.SameSite = mygo.CookieSameSiteStrict
	case "none":
		c.SameSite = mygo.CookieSameSiteNone
	default:
		return c, &Error{Code: CodeBadRequest, Message: "same_site must be lax, strict or none"}
	}
	if o.Expires != "" {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, o.Expires); err == nil {
				c.Expires = t
				break
			}
		}
		if c.Expires.IsZero() {
			return c, &Error{Code: CodeBadRequest, Message: "expires must be RFC3339 or YYYY-MM-DD"}
		}
	}
	return c, nil
}

func cookieInfos(list []mygo.Cookie) []CookieInfo {
	out := make([]CookieInfo, 0, len(list))
	for _, c := range list {
		ci := CookieInfo{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: string(c.SameSite),
		}
		if !c.Expires.IsZero() {
			ci.Expires = c.Expires.Format(time.RFC3339)
		}
		out = append(out, ci)
	}
	return out
}

func cookieErr(err error) error {
	return &Error{Code: CodeInternal, Message: "cookies: " + err.Error()}
}

// CookieHeader formats cookies for display or requests.
func CookieHeader(list []CookieInfo) string {
	parts := make([]string, 0, len(list))
	for _, c := range list {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}
