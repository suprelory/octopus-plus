package router

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"golang.org/x/net/publicsuffix"
)

// Cookies is operation-local. A complete snapshot, including expired cookies,
// prevents a subsequent run from resurrecting a deleted seed credential.
type Cookies struct {
	mu     sync.Mutex
	jar    *cookiejar.Jar
	origin string
	state  []model.CheckinSessionCookie
}

func NewCookies(baseURL, seed string, state []model.CheckinSessionCookie) *Cookies {
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return &Cookies{jar: jar}
	}
	c := &Cookies{jar: jar, origin: u.Scheme + "://" + u.Host}
	if len(state) == 0 && seed != "" {
		r := &http.Request{Header: http.Header{"Cookie": []string{seed}}}
		cookies := r.Cookies()
		for _, cookie := range cookies {
			cookie.Path = "/"
		}
		c.SetCookies(u, cookies)
	} else {
		for _, item := range state {
			origin, err := url.Parse(item.URL)
			if err == nil {
				c.SetCookies(origin, []*http.Cookie{&item.Cookie})
			}
		}
	}
	return c
}

func (c *Cookies) Cookies(u *url.URL) []*http.Cookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	if u.Scheme+"://"+u.Host != c.origin {
		return nil
	}
	return c.jar.Cookies(u)
}

func (c *Cookies) SetCookies(u *url.URL, cookies []*http.Cookie) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if u.Scheme+"://"+u.Host != c.origin {
		return
	}
	for _, incoming := range cookies {
		cookie := *incoming
		if cookie.Valid() != nil {
			continue
		}
		if !strings.HasPrefix(cookie.Path, "/") {
			cookie.Path = "/"
			if i := strings.LastIndex(u.Path, "/"); i > 0 {
				cookie.Path = u.Path[:i]
			}
		}
		if cookie.MaxAge > 0 {
			cookie.Expires = time.Now().UTC().Add(time.Duration(min(cookie.MaxAge, 315360000)) * time.Second)
			cookie.MaxAge = 0
		}
		cookie.Raw, cookie.RawExpires, cookie.Unparsed = "", "", nil
		// Use the standard jar to reject foreign domains and public suffixes.
		probe, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		marker := cookie
		marker.Value, marker.MaxAge, marker.Expires = "accepted", 0, time.Time{}
		probe.SetCookies(u, []*http.Cookie{&marker})
		target := *u
		target.Path = cookie.Path
		if cookie.Secure {
			target.Scheme = "https"
		}
		if len(probe.Cookies(&target)) == 0 {
			continue
		}
		index := -1
		for i, previous := range c.state {
			if previous.Cookie.Name == cookie.Name && previous.Cookie.Path == cookie.Path &&
				cookieDomain(previous.Cookie.Domain, u.Hostname()) == cookieDomain(cookie.Domain, u.Hostname()) {
				index = i
				break
			}
		}
		item := model.CheckinSessionCookie{URL: c.origin + u.EscapedPath(), Cookie: cookie}
		if index >= 0 {
			c.state[index] = item
		} else if len(c.state) < 64 {
			c.state = append(c.state, item)
		} else {
			continue
		}
		c.jar.SetCookies(u, []*http.Cookie{&cookie})
	}
}

func cookieDomain(domain, host string) string {
	if domain == "" {
		domain = host
	}
	return strings.ToLower(strings.TrimPrefix(domain, "."))
}

func (c *Cookies) Snapshot() []model.CheckinSessionCookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.CheckinSessionCookie(nil), c.state...)
}

func (c *Cookies) Header(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	r := &http.Request{Header: make(http.Header)}
	for _, cookie := range c.Cookies(u) {
		r.AddCookie(cookie)
	}
	return r.Header.Get("Cookie")
}

func HasSession(header string) bool {
	r := &http.Request{Header: http.Header{"Cookie": []string{header}}}
	for _, cookie := range r.Cookies() {
		name := strings.ToLower(cookie.Name)
		if cookie.Value != "" && (strings.Contains(name, "session") || strings.Contains(name, "token") || strings.Contains(name, "auth") || name == "jwt") {
			return true
		}
	}
	return false
}
