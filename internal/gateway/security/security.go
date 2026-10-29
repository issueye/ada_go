// Package security 是 Gateway 的 Web 安全中间件（设计 9.3）。
//
// 它只做边界检查，不关心页面内容：
//   - 放行健康检查、登录页与静态资源；
//   - Cookie 或 Bearer 单用户 token 鉴权，失败时 API 返 401、页面 303 到登录页；
//   - 非安全方法校验 Origin 与 CSRF（双提交 cookie），阻断跨站请求；
//   - 统一安全响应头、禁用缓存、限制请求体大小。
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// 默认值（设计 9.3、9.5）。
const (
	DefaultCookieName   = "adag_session"
	DefaultCSRFName     = "adag_csrf"
	DefaultMaxBodyBytes = 1 << 20
	DefaultLoginPath    = "/login"

	csrfHeader = "X-CSRF-Token"
	csrfField  = "csrf_token"

	sessionMaxAge = 30 * 24 * time.Hour
	cspPolicy     = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'"
)

// Config 是中间件的装配输入。零值字段使用默认值。
type Config struct {
	// Token 是 v1 单用户访问令牌；必填。
	Token string
	// CookieName 是会话 cookie 名，默认 adag_session。
	CookieName string
	// CSRFName 是 CSRF 双提交 cookie 名，默认 adag_csrf。
	CSRFName string
	// Secure 控制 cookie 的 Secure 属性（非回环部署应为 true）。
	Secure bool
	// AllowedOrigins 是额外允许的跨源 Origin（如 https://ui.example）。
	AllowedOrigins []string
	// MaxBodyBytes 是请求体上限，默认 1MB。
	MaxBodyBytes int64
	// LoginPath 是未登录页面的跳转目标，默认 /login。
	LoginPath string
}

// Middleware 实现鉴权、CSRF、Origin 与安全头检查。
type Middleware struct {
	token      string
	cookieName string
	csrfName   string
	secure     bool
	origins    map[string]struct{}
	maxBody    int64
	loginPath  string
}

// New 校验配置并返回中间件。
func New(cfg Config) (*Middleware, error) {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, errors.New("security: 需要非空 Token")
	}
	m := &Middleware{
		token:      token,
		cookieName: cfg.CookieName,
		csrfName:   cfg.CSRFName,
		secure:     cfg.Secure,
		origins:    map[string]struct{}{},
		maxBody:    cfg.MaxBodyBytes,
		loginPath:  cfg.LoginPath,
	}
	if m.cookieName == "" {
		m.cookieName = DefaultCookieName
	}
	if m.csrfName == "" {
		m.csrfName = DefaultCSRFName
	}
	if m.maxBody <= 0 {
		m.maxBody = DefaultMaxBodyBytes
	}
	if m.loginPath == "" {
		m.loginPath = DefaultLoginPath
	}
	if !strings.HasPrefix(m.loginPath, "/") || m.loginPath == "/" {
		return nil, fmt.Errorf("security: LoginPath %q 必须是具体路径", m.loginPath)
	}
	for _, raw := range cfg.AllowedOrigins {
		origin, err := normalizeOrigin(raw)
		if err != nil {
			return nil, err
		}
		m.origins[origin] = struct{}{}
	}
	return m, nil
}

// Wrap 包装下游 handler。
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.setHeaders(w, r)

		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, m.maxBody)
			if r.ContentLength > m.maxBody {
				m.fail(w, r, http.StatusRequestEntityTooLarge, "request_too_large")
				return
			}
		}

		if m.bypass(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		viaCookie, ok := m.authenticate(r)
		if !ok {
			m.unauthorized(w, r)
			return
		}

		// Origin 只对会改变状态的方法检查（9.3）。
		if !isSafeMethod(r.Method) && !m.originAllowed(r) {
			m.fail(w, r, http.StatusForbidden, "origin_denied")
			return
		}

		// Bearer 不是浏览器自动携带的凭据，因此不需要 CSRF。
		if viaCookie && !isSafeMethod(r.Method) {
			token, err := m.csrfToken(r)
			if err != nil {
				if errors.Is(err, errBodyTooLarge) {
					m.fail(w, r, http.StatusRequestEntityTooLarge, "request_too_large")
					return
				}
				token = ""
			}
			if token == "" || !secureEqual(token, m.CSRFToken(r)) {
				m.fail(w, r, http.StatusForbidden, "csrf_invalid")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// CSRFToken 返回当前请求携带的 CSRF cookie 值；没有时返回空串。
func (m *Middleware) CSRFToken(r *http.Request) string {
	if c, err := r.Cookie(m.csrfName); err == nil {
		return c.Value
	}
	return ""
}

// CheckToken 常量时间校验一个访问令牌（桌面壳/托盘的 `?token=` 自动登录用）。
func (m *Middleware) CheckToken(token string) bool {
	return m.token != "" && secureEqual(token, m.token)
}

// IssueSession 下发会话 cookie（v1 单用户：值就是 Token）与随机 CSRF cookie。
func (m *Middleware) IssueSession(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    m.token,
		Path:     "/",
		MaxAge:   int(sessionMaxAge / time.Second),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     m.csrfName,
		Value:    newCSRFToken(),
		Path:     "/",
		MaxAge:   int(sessionMaxAge / time.Second),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// setHeaders 统一安全响应头；静态资源允许缓存，其余一律 no-store。
func (m *Middleware) setHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", cspPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	if !strings.HasPrefix(r.URL.Path, "/static/") {
		h.Set("Cache-Control", "no-store")
	}
}

func (m *Middleware) bypass(path string) bool {
	if path == "/healthz" || path == m.loginPath {
		return true
	}
	if strings.HasPrefix(path, m.loginPath+"/") {
		return true
	}
	return strings.HasPrefix(path, "/static/")
}

// authenticate 返回是否通过 Cookie 会话认证，以及是否认证成功。
func (m *Middleware) authenticate(r *http.Request) (viaCookie, ok bool) {
	if token, found := bearerToken(r); found && secureEqual(token, m.token) {
		return false, true
	}
	if c, err := r.Cookie(m.cookieName); err == nil && secureEqual(c.Value, m.token) {
		return true, true
	}
	return false, false
}

func (m *Middleware) originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// 非浏览器客户端或同源表单不带 Origin；CSRF 仍会约束 Cookie 会话。
		return true
	}
	// Chromium 在 Referrer-Policy: no-referrer 下会把表单 POST 的 Origin 发成 "null"
	// （桌面壳正好命中）。Sec-Fetch-Site 由浏览器写入、脚本不可伪造：只有
	// same-origin 才放行；sandbox iframe 之类的不透明来源不会是同源。
	if strings.EqualFold(origin, "null") {
		return strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "same-origin")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if isLoopbackHost(u.Hostname()) {
		return true
	}
	if _, ok := m.origins[strings.ToLower(u.Scheme+"://"+u.Host)]; ok {
		return true
	}
	// 与请求 Host 同源。
	return strings.EqualFold(u.Host, r.Host)
}

var errBodyTooLarge = errors.New("security: 请求体超限")

// csrfToken 读取 X-CSRF-Token，或表单里的 csrf_token。
//
// 表单解析会消费请求体；只有表单类型才解析，避免破坏 JSON API 的 body。
func (m *Middleware) csrfToken(r *http.Request) (string, error) {
	if token := r.Header.Get(csrfHeader); token != "" {
		return token, nil
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	switch {
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		if err := r.ParseForm(); err != nil {
			if isMaxBytesError(err) {
				return "", errBodyTooLarge
			}
			return "", err
		}
		return r.PostForm.Get(csrfField), nil
	case strings.HasPrefix(ct, "multipart/form-data"):
		if err := r.ParseMultipartForm(m.maxBody); err != nil {
			if isMaxBytesError(err) {
				return "", errBodyTooLarge
			}
			return "", err
		}
		if token := r.PostForm.Get(csrfField); token != "" {
			return token, nil
		}
		return r.Form.Get(csrfField), nil
	default:
		return "", nil
	}
}

func (m *Middleware) unauthorized(w http.ResponseWriter, r *http.Request) {
	if isAPI(r) {
		m.fail(w, r, http.StatusUnauthorized, "unauthorized")
		return
	}
	http.Redirect(w, r, m.loginPath, http.StatusSeeOther)
}

// fail 对 API 返回 JSON 错误，对页面返回纯文本；两者都保留状态码。
func (m *Middleware) fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	if isAPI(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		body := map[string]any{
			"error": map[string]any{"code": status, "message": message},
		}
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	http.Error(w, message, status)
}

func isAPI(r *http.Request) bool {
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(auth[len(prefix):]), true
}

func secureEqual(a, b string) bool {
	if a == "" || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("security: AllowedOrigins 含空值")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("security: AllowedOrigins 项 %q 无法解析", raw)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

var csrfFallback atomic.Uint64

func newCSRFToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), csrfFallback.Add(1))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
