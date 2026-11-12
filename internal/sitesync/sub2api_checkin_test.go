package sitesync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func configureSub2APITestCheckin(t *testing.T, ctx context.Context, site *model.Site, source *model.SiteAccount, status bool) {
	t.Helper()
	refresh := "rt_old"
	if _, err := updateSub2APITestAccount(&model.SiteAccountUpdateRequest{ID: source.ID, RefreshToken: &refresh}, ctx); err != nil {
		t.Fatal(err)
	}
	headers := []model.CustomHeader{{HeaderKey: "User-Agent", HeaderValue: "subscription-agent"}}
	if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: source.SiteID, CustomHeader: &headers}, ctx); err != nil {
		t.Fatal(err)
	}
	if status {
		config := &model.SiteCheckinStatusConfig{Path: "/status", CheckedPath: "data.checked_in", MaxAttempts: 1, PollIntervalMs: 1,
			Headers: []model.CustomHeader{{HeaderKey: "User-Agent", HeaderValue: "ignored-checkin-agent"}}}
		if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinStatus: config, CheckinStatusSet: true}, ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSub2APICheckinRecovers401BeforeOrAfterSubmission(t *testing.T) {
	for _, afterSubmission := range []bool{false, true} {
		name := "before"
		if afterSubmission {
			name = "after"
		}
		t.Run(name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var refreshes, submits, queries atomic.Int32
			subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/refresh" {
					refreshes.Add(1)
					writeSub2APITestRefresh(t, w, r)
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{"balance":10}}`))
			}))
			defer subscription.Close()
			checkin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("User-Agent") != "subscription-agent" || r.Header.Get("Cookie") != "" {
					t.Error("checkin/status did not inherit the subscription session's UA")
				}
				if r.URL.Path == "/daily" {
					submits.Add(1)
					if afterSubmission {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"reason":"TOKEN_EXPIRED","message":"expired"}`))
					} else {
						_, _ = w.Write([]byte(`{"success":true}`))
					}
					return
				}
				queries.Add(1)
				if (!afterSubmission || submits.Load() > 0) && r.Header.Get("Authorization") != "Bearer new-access-token" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"reason":"TOKEN_EXPIRED","message":"expired"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": submits.Load() > 0}})
			}))
			defer checkin.Close()
			site, account, source := subscriptionTokenCheckinFixture(t, ctx, subscription.URL, checkin.URL)
			configureSub2APITestCheckin(t, ctx, site, source, true)
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || refreshes.Load() != 1 || submits.Load() != 1 || queries.Load() != 3 {
				t.Fatalf("status recovery failed: result=%+v err=%v refreshes=%d submits=%d queries=%d", result, err, refreshes.Load(), submits.Load(), queries.Load())
			}
			saved, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil || saved.AccessToken != "" || saved.RefreshToken != "" || saved.CheckinPending == nil || !saved.CheckinPending.Submitted {
				t.Fatalf("workflow or credential ownership changed: %v", err)
			}
		})
	}
}

func TestSub2APICheckinWithoutStatusNeverResendsGETSubmission(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var submits, refreshes atomic.Int32
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			refreshes.Add(1)
			writeSub2APITestRefresh(t, w, r)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"balance":10}}`))
	}))
	defer subscription.Close()
	checkin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submits.Add(1)
		if r.Method != http.MethodGet {
			t.Error("test did not use GET submission")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"reason":"TOKEN_EXPIRED","message":"expired"}`))
	}))
	defer checkin.Close()
	site, account, source := subscriptionTokenCheckinFixture(t, ctx, subscription.URL, checkin.URL)
	configureSub2APITestCheckin(t, ctx, site, source, false)
	method := http.MethodGet
	if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinHTTPMethod: &method}, ctx); err != nil {
		t.Fatal(err)
	}
	result, _ := CheckinAccount(ctx, account.ID)
	if result == nil || result.Status != model.SiteExecutionStatusFailed || !strings.Contains(result.Message, "未自动重发") || submits.Load() != 1 || refreshes.Load() != 1 {
		t.Fatalf("GET submission was retried or its outcome hidden: %+v submits=%d refreshes=%d", result, submits.Load(), refreshes.Load())
	}
}

func TestSub2APICheckinInvalidRefreshPreservesSubmittedWorkflow(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes, submits atomic.Int32
	var repaired atomic.Bool
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			refreshes.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"reason":"REFRESH_TOKEN_INVALID","message":"already consumed"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"balance":10}}`))
	}))
	defer subscription.Close()
	checkin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/daily" {
			submits.Add(1)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if submits.Load() > 0 && !repaired.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"reason":"TOKEN_EXPIRED","message":"expired"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": submits.Load() > 0}})
	}))
	defer checkin.Close()
	site, account, source := subscriptionTokenCheckinFixture(t, ctx, subscription.URL, checkin.URL)
	configureSub2APITestCheckin(t, ctx, site, source, true)
	first, err := CheckinAccount(ctx, account.ID)
	if err != nil || first == nil || first.Status != model.SiteExecutionStatusPending || !strings.Contains(first.Message, "Refresh Token") || submits.Load() != 1 || refreshes.Load() != 1 {
		t.Fatalf("invalid refresh lost a submitted checkin: %+v %v", first, err)
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.CheckinPending == nil || !saved.CheckinPending.Submitted {
		t.Fatalf("submitted reservation lost: %v", err)
	}
	access, refresh := "manual-access", "rt_manual"
	if _, err := updateSub2APITestAccount(&model.SiteAccountUpdateRequest{ID: source.ID, AccessToken: &access, RefreshToken: &refresh}, ctx); err != nil {
		t.Fatal(err)
	}
	repaired.Store(true)
	next, err := CheckinAccount(ctx, account.ID)
	if err != nil || next == nil || next.Status != model.SiteExecutionStatusSuccess || next.LogID != first.LogID || submits.Load() != 1 || refreshes.Load() != 1 {
		t.Fatalf("repaired session resubmitted the checkin: %+v %v submits=%d", next, err, submits.Load())
	}
}
