package model

import "testing"

func TestCheckinSiteValidateNormalizesCheckinScheduleDefaults(t *testing.T) {
	site := &CheckinSite{
		Name:     "schedule-defaults",
		Platform: SitePlatformNewAPI,
		BaseURL:  "https://example.com",
	}
	if err := site.Validate(); err != nil {
		t.Fatalf("CheckinSite.Validate failed: %v", err)
	}
	if site.CheckinTimezone != DefaultSiteCheckinTimezone ||
		site.CheckinWindowStart != DefaultSiteCheckinWindowStart ||
		site.CheckinWindowEnd != DefaultSiteCheckinWindowEnd {
		t.Fatalf("unexpected checkin defaults: timezone=%q start=%q end=%q", site.CheckinTimezone, site.CheckinWindowStart, site.CheckinWindowEnd)
	}
}

func TestCheckinSiteValidateRejectsInvalidCheckinSchedule(t *testing.T) {
	tests := []struct {
		name     string
		timezone string
		start    string
		end      string
	}{
		{name: "invalid timezone", timezone: "Not/A_Timezone", start: "08:00", end: "23:00"},
		{name: "invalid start", timezone: "Asia/Shanghai", start: "8am", end: "23:00"},
		{name: "end before start", timezone: "Asia/Shanghai", start: "20:00", end: "08:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			site := &CheckinSite{
				Name:               "invalid-schedule",
				Platform:           SitePlatformNewAPI,
				BaseURL:            "https://example.com",
				CheckinTimezone:    tt.timezone,
				CheckinWindowStart: tt.start,
				CheckinWindowEnd:   tt.end,
			}
			if err := site.Validate(); err == nil {
				t.Fatalf("expected invalid checkin schedule to fail validation")
			}
		})
	}
}

func TestCheckinSiteValidateCustomHTTPCheckinConfig(t *testing.T) {
	baseSite := func() *CheckinSite {
		return &CheckinSite{
			Name:               "custom-http-checkin",
			Platform:           SitePlatformAPI,
			BaseURL:            "https://example.com/prefix",
			CheckinHTTPEnabled: true,
			CheckinHTTPMethod:  "post",
			CheckinHTTPPath:    "/api/checkin?day=today",
			CheckinHTTPBody:    `{"token":"{{access_token}}"}`,
			CheckinHTTPHeaders: []CustomHeader{{HeaderKey: "X-Account", HeaderValue: "{{username}}"}},
		}
	}
	if err := baseSite().Validate(); err != nil {
		t.Fatalf("valid custom HTTP checkin rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*CheckinSite)
	}{
		{name: "unsupported method", mutate: func(site *CheckinSite) { site.CheckinHTTPMethod = "PUT" }},
		{name: "absolute path", mutate: func(site *CheckinSite) { site.CheckinHTTPPath = "https://evil.example/checkin" }},
		{name: "path traversal", mutate: func(site *CheckinSite) { site.CheckinHTTPPath = "/api/../admin" }},
		{name: "invalid body", mutate: func(site *CheckinSite) { site.CheckinHTTPBody = "{bad" }},
		{name: "body with GET", mutate: func(site *CheckinSite) { site.CheckinHTTPMethod = "GET" }},
		{name: "reserved header", mutate: func(site *CheckinSite) {
			site.CheckinHTTPHeaders = []CustomHeader{{HeaderKey: "Content-Length", HeaderValue: "1"}}
		}},
		{name: "header newline", mutate: func(site *CheckinSite) {
			site.CheckinHTTPHeaders = []CustomHeader{{HeaderKey: "X-Test", HeaderValue: "ok\r\nInjected: yes"}}
		}},
		{name: "duplicate header", mutate: func(site *CheckinSite) {
			site.CheckinHTTPHeaders = []CustomHeader{{HeaderKey: "X-Test", HeaderValue: "one"}, {HeaderKey: "x-test", HeaderValue: "two"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			site := baseSite()
			tt.mutate(site)
			if err := site.Validate(); err == nil {
				t.Fatal("expected invalid custom HTTP checkin configuration to fail validation")
			}
		})
	}
}

func TestCheckinSiteModeValidation(t *testing.T) {
	for _, tc := range []struct {
		mode     SiteCheckinMode
		platform SitePlatform
		valid    bool
	}{
		{"", SitePlatformDoneHub, true}, {SiteCheckinModeEnabled, SitePlatformDoneHub, true},
		{SiteCheckinModeEnabled, SitePlatformAPI, false}, {"invalid", SitePlatformNewAPI, false},
	} {
		site := CheckinSite{Name: "CheckinSite", Platform: tc.platform, BaseURL: "https://site.example", CheckinMode: tc.mode}
		if err := site.Validate(); (err == nil) != tc.valid {
			t.Fatalf("mode %q, platform %q: %v", tc.mode, tc.platform, err)
		}
	}
}
