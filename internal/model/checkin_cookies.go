package model

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

func (c CheckinCredential) ValidateSessionCookies() error {
	if len(c.SessionCookies) == 0 {
		return nil
	}
	if len(c.SessionCookies) > 64 {
		return errors.New("checkin cookie state exceeds its limit")
	}
	encoded, err := json.Marshal(c.SessionCookies)
	if err != nil || len(encoded) > 64<<10 {
		return errors.New("checkin cookie state exceeds its limit")
	}
	base, err := url.Parse(c.AuthBaseURL)
	if err != nil {
		return errors.New("invalid cookie origin")
	}
	for _, item := range c.SessionCookies {
		u, err := url.Parse(item.URL)
		if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			item.Cookie.Valid() != nil || !strings.HasPrefix(item.Cookie.Path, "/") || item.Cookie.MaxAge > 0 {
			return errors.New("invalid scoped checkin cookie")
		}
	}
	return nil
}
