package checkin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/finance"
	"github.com/suprelory/octopus-plus/internal/checkin/router"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

type BalanceStore interface {
	BeginBalance(context.Context, int) (*op.CheckinBalanceSnapshot, error)
	SaveBalance(context.Context, *op.CheckinBalanceSnapshot, finance.Balance, op.CheckinLoginSession) error
	SaveBalanceSession(context.Context, *op.CheckinBalanceSnapshot, op.CheckinLoginSession) error
	FillBalanceReward(context.Context, *model.CheckinResult, *op.CheckinBalanceSnapshot, *op.CheckinBalanceSnapshot, string) (bool, error)
}

type Balances struct {
	store    BalanceStore
	sessions SessionProvider
	proxies  ProxyResolver
}

type BalanceObservation struct {
	finance.Balance
	Snapshot *op.CheckinBalanceSnapshot `json:"-"`
}

func NewBalances(store BalanceStore, sessions SessionProvider, proxies ProxyResolver) *Balances {
	return &Balances{store: store, sessions: sessions, proxies: proxies}
}

var ErrBalanceResponse = errors.New("upstream balance is unavailable or invalid")

func balanceClient(proxies ProxyResolver, s *op.CheckinBalanceSnapshot, credential model.CheckinCredential) *platformHTTP {
	c := credential
	seed := c.Cookie
	if seed == "" && router.HasSession(c.AccessToken) {
		seed = c.AccessToken
	}
	return &platformHTTP{proxies: proxies, base: s.Config.BaseURL, network: s.Config.CheckinNetwork, headers: s.Config.Headers, original: c,
		session: op.CheckinLoginSession{AccessToken: c.AccessToken, PlatformUserID: c.PlatformUserID, TokenExpiresAt: c.TokenExpiresAt},
		cookies: router.NewCookies(c.AuthBaseURL, seed, c.SessionCookies)}
}

// Query has its own deadline and never turns AgentRouter balance reads into a
// login/check-in. Optional income failures leave the previous TodayIncome intact.
func (b *Balances) Query(ctx context.Context, id int, includeIncome bool) (*BalanceObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot, err := b.store.BeginBalance(ctx, id)
	if err != nil {
		return nil, err
	}
	session, err := b.sessions.Ensure(ctx, snapshot.Credential.ID)
	if err != nil {
		return nil, err
	}
	if session.Credential.ID != snapshot.Credential.ID || session.Credential.IdentityRevision != snapshot.Credential.IdentityRevision {
		return nil, op.ErrCheckinBalanceChanged
	}
	observation := snapshot.Credential.BalanceObservation
	snapshot.Credential = session.Credential
	snapshot.Credential.BalanceObservation = observation
	p := balanceClient(b.proxies, snapshot, session.Credential)
	saved := false
	defer func() {
		if !saved {
			persist, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			_ = b.store.SaveBalanceSession(persist, snapshot, p.snapshot())
		}
	}()
	recovered := false
	read := func(path string) (map[string]any, error) {
		for {
			prepared, err := p.prepare(ctx, http.MethodGet, path, nil, false)
			if err != nil {
				return nil, err
			}
			if path == "/api/user/self" && p.session.PlatformUserID == nil {
				setPlatformUser(prepared.request, profileIDHint(p.original.AccessToken))
			}
			response, payload, err := p.send(prepared)
			if err != nil {
				return nil, err
			}
			if response.StatusCode == http.StatusUnauthorized && !recovered {
				recovered = true
				session, err := b.sessions.RecoverRead(ctx, Session{Credential: p.original})
				if err != nil {
					return nil, err
				}
				if session.Credential.IdentityRevision != snapshot.Credential.IdentityRevision {
					return nil, op.ErrCheckinBalanceChanged
				}
				observation := snapshot.Credential.BalanceObservation
				snapshot.Credential = session.Credential
				snapshot.Credential.BalanceObservation = observation
				p = balanceClient(b.proxies, snapshot, session.Credential)
				continue
			}
			if response.StatusCode < 200 || response.StatusCode >= 300 || payload == nil {
				return nil, ErrBalanceResponse
			}
			if success, exists := payload["success"]; exists && success != true {
				return nil, ErrBalanceResponse
			}
			return payload, nil
		}
	}
	path := "/api/user/self"
	if snapshot.Config.Platform == model.SitePlatformSub2API {
		path = "/api/v1/auth/me"
	}
	payload, err := read(path)
	if err != nil {
		return nil, err
	}
	data, wrapped := payload["data"].(map[string]any)
	var value finance.Balance
	if snapshot.Config.Platform == model.SitePlatformSub2API {
		if code, exists := payload["code"]; exists {
			number, ok := finance.Number(code)
			if !ok || number != 0 || !wrapped {
				return nil, ErrBalanceResponse
			}
		}
		if !wrapped {
			data = payload
		}
		var ok bool
		value.Value, ok = finance.Number(data["balance"])
		if !ok {
			return nil, ErrBalanceResponse
		}
	} else {
		if !wrapped {
			return nil, ErrBalanceResponse
		}
		if id := platformUserID(payload); id != nil {
			p.session.PlatformUserID = id
		}
		scale := finance.QuotaPerUSD
		if snapshot.Config.Platform == model.SitePlatformAgentRouter {
			if status, err := read("/api/status"); err == nil {
				data, _ := status["data"].(map[string]any)
				if number, ok := finance.Number(data["quota_per_unit"]); ok && number > 0 {
					scale = number
				}
			}
		}
		remaining := snapshot.Config.Platform != model.SitePlatformOneAPI && snapshot.Config.Platform != model.SitePlatformOneHub
		var ok bool
		value, ok = finance.Quota(data, scale, remaining)
		if !ok {
			return nil, ErrBalanceResponse
		}
		if includeIncome && !value.IncomeKnown && supportsBalanceIncome(snapshot.Config.Platform) {
			value.Income, value.IncomeKnown = balanceIncome(read, time.Now(), scale)
		}
	}
	persist, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if err := b.store.SaveBalance(persist, snapshot, value, p.snapshot()); err != nil {
		return nil, err
	}
	saved = true
	return &BalanceObservation{Balance: value, Snapshot: snapshot}, nil
}

func supportsBalanceIncome(platform model.SitePlatform) bool {
	return platform == model.SitePlatformNewAPI || platform == model.SitePlatformAnyRouter || platform == model.SitePlatformAgentRouter || platform == model.SitePlatformOneAPI
}

func balanceIncome(read func(string) (map[string]any, error), now time.Time, scale float64) (float64, bool) {
	// TodayIncome retains the upstream/server-local calendar, independent of the
	// check-in site's workflow date and locally aggregated rewards.
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, 1).Add(-time.Second)
	var total float64
	for _, kind := range []int{1, 4} {
		complete := false
		for page := 1; page <= 6; page++ {
			payload, err := read(fmt.Sprintf("/api/log/self?p=%d&page_size=100&type=%d&token_name=&model_name=&start_timestamp=%d&end_timestamp=%d&group=", page, kind, start.Unix(), end.Unix()))
			if err != nil {
				return 0, false
			}
			data, _ := payload["data"].(map[string]any)
			var items []any
			for _, candidate := range []any{data["items"], payload["items"], payload["data"]} {
				if list, ok := candidate.([]any); ok {
					items = list
					break
				}
			}
			if items == nil {
				return 0, false
			}
			for _, item := range items {
				entry, ok := item.(map[string]any)
				if !ok {
					return 0, false
				}
				if quota, ok := finance.Number(entry["quota"]); ok && quota > 0 {
					total += quota / scale
				} else {
					total += finance.Income(stringValue(entry["content"]))
				}
			}
			count, known := finance.Number(data["total"])
			if !known {
				count, known = finance.Number(payload["total"])
			}
			if len(items) == 0 || known && count >= 0 && count == math.Trunc(count) && float64(page*100) >= count {
				complete = true
				break
			}
		}
		if !complete {
			return 0, false
		}
	}
	if !finance.Finite(total * 1e6) {
		return 0, false
	}
	return math.Round(total*1e6) / 1e6, true
}
