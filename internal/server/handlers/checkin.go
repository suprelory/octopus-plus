package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/checkin"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
)

type checkinAPI struct{ services *checkin.Services }

// RegisterCheckinRoutes is explicit until P5 runtime cutover. It requires a
// prepared independent database and one long-lived Services instance. There is
// no init registration, schema migration, legacy fallback or task activation.
func RegisterCheckinRoutes(engine *gin.Engine, services *checkin.Services) error {
	if engine == nil || services == nil || services.Store == nil || services.Engine == nil || services.Balances == nil || services.Jobs == nil || services.Notifications == nil {
		return errors.New("check-in API requires initialized independent services")
	}
	a := &checkinAPI{services: services}
	group := engine.Group("/api/v1/checkin", middleware.Auth())
	for _, route := range []struct {
		method, path string
		handle       gin.HandlerFunc
	}{
		{http.MethodGet, "/sites", a.listSites}, {http.MethodPost, "/sites", a.createSite},
		{http.MethodGet, "/sites/:id", a.getSite}, {http.MethodPatch, "/sites/:id", a.patchSite}, {http.MethodDelete, "/sites/:id", a.deleteSite},
		{http.MethodPost, "/sites/:id/archive", a.archiveSite}, {http.MethodPost, "/sites/:id/restore", a.restoreSite},
		{http.MethodGet, "/accounts", a.listAccounts}, {http.MethodPost, "/accounts", a.createAccount},
		{http.MethodGet, "/accounts/:id", a.getAccount}, {http.MethodPatch, "/accounts/:id", a.patchAccount}, {http.MethodDelete, "/accounts/:id", a.deleteAccount},
		{http.MethodGet, "/credentials", a.listCredentials}, {http.MethodPost, "/credentials", a.createCredential},
		{http.MethodGet, "/credentials/:id", a.getCredential}, {http.MethodPatch, "/credentials/:id", a.patchCredential}, {http.MethodDelete, "/credentials/:id", a.deleteCredential},
		{http.MethodPost, "/accounts/:id/execute", a.execute}, {http.MethodPost, "/accounts/:id/confirm", a.confirm}, {http.MethodPost, "/accounts/:id/balance", a.balance},
		{http.MethodGet, "/batches", a.listBatches}, {http.MethodPost, "/batches", a.startBatch}, {http.MethodGet, "/batches/:id", a.getBatch},
		{http.MethodGet, "/logs", a.listLogs}, {http.MethodGet, "/stats", a.stats}, {http.MethodGet, "/capabilities", a.capabilities},
		{http.MethodPost, "/reward/test", a.testReward}, {http.MethodPost, "/status/test", a.testStatus},
		{http.MethodGet, "/notifications", a.listNotifications}, {http.MethodPost, "/notifications/:id/retry", a.retryNotification},
	} {
		group.Handle(route.method, route.path, route.handle)
	}
	return nil
}
