package handlers

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/server/resp"
)

// A distinct field type avoids promoting Patch.UnmarshalJSON onto the create
// envelope, which would otherwise swallow site_id and inline credentials.
type checkinAccountFields model.CheckinAccountPatch
type checkinAccountInput struct {
	checkinAccountFields
	SiteID     model.CheckinPatchField[int]  `json:"site_id"`
	Credential *model.CheckinCredentialInput `json:"credential,omitempty"`
}

func (a *checkinAPI) listSites(c *gin.Context) {
	query, ok := checkinQuery(c, "include_archived")
	if !ok {
		return
	}
	archived := false
	if raw := query.Get("include_archived"); raw != "" {
		if raw != "true" && raw != "false" {
			resp.InvalidParam(c)
			return
		}
		archived = raw == "true"
	}
	sites, err := a.services.Store.ListSites(c.Request.Context(), archived)
	if err != nil {
		checkinError(c, err)
		return
	}
	items := make([]checkinSiteResponse, 0, len(sites))
	for _, site := range sites {
		items = append(items, checkinSiteDTO(&site))
	}
	resp.Success(c, items)
}

func (a *checkinAPI) getSite(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok {
		return
	}
	if _, ok := checkinQuery(c); !ok {
		return
	}
	site, err := a.services.Store.GetSite(c.Request.Context(), id)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, checkinSiteDTO(site))
}

func (a *checkinAPI) createSite(c *gin.Context) {
	if _, ok := checkinQuery(c); !ok {
		return
	}
	var request model.CheckinSitePatch
	if !bindCheckinJSON(c, &request, false, 1<<20) {
		return
	}
	site := model.CheckinSite{Enabled: true}
	request.Apply(&site)
	created, err := a.services.Store.CreateSite(c.Request.Context(), site)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, checkinSiteDTO(created))
}

func (a *checkinAPI) patchSite(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok {
		return
	}
	if _, ok := checkinQuery(c); !ok {
		return
	}
	var request model.CheckinSitePatch
	if !bindCheckinJSON(c, &request, false, 1<<20) {
		return
	}
	site, err := a.services.Store.PatchSite(c.Request.Context(), id, request)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, checkinSiteDTO(site))
}

func (a *checkinAPI) deleteSite(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok || !checkinEmptyAction(c) {
		return
	}
	if err := a.services.Store.DeleteSite(c.Request.Context(), id); err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, gin.H{"id": id})
}

func (a *checkinAPI) archiveSite(c *gin.Context) { a.setArchived(c, true) }
func (a *checkinAPI) restoreSite(c *gin.Context) { a.setArchived(c, false) }
func (a *checkinAPI) setArchived(c *gin.Context, archived bool) {
	id, ok := checkinPathID(c)
	if !ok || !checkinEmptyAction(c) {
		return
	}
	var site *model.CheckinSite
	var err error
	if archived {
		site, err = a.services.Store.ArchiveSite(c.Request.Context(), id)
	} else {
		site, err = a.services.Store.RestoreSite(c.Request.Context(), id)
	}
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, checkinSiteDTO(site))
}

func (a *checkinAPI) listAccounts(c *gin.Context) {
	query, ok := checkinQuery(c, "site_id")
	if !ok {
		return
	}
	siteID := int64(0)
	if raw := query.Get("site_id"); raw != "" {
		siteID, ok = checkinPositiveID(raw, strconv.IntSize)
		if !ok {
			resp.InvalidParam(c)
			return
		}
	}
	accounts, err := a.services.Store.ListAccounts(c.Request.Context(), int(siteID))
	if err != nil {
		checkinError(c, err)
		return
	}
	items := make([]checkinAccountResponse, 0, len(accounts))
	for _, account := range accounts {
		items = append(items, checkinAccountDTO(&account))
	}
	resp.Success(c, items)
}

func (a *checkinAPI) getAccount(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok {
		return
	}
	if _, ok := checkinQuery(c); !ok {
		return
	}
	account, err := a.services.Store.GetAccount(c.Request.Context(), id)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, checkinAccountDTO(account))
}

func (a *checkinAPI) createAccount(c *gin.Context) {
	if _, ok := checkinQuery(c); !ok {
		return
	}
	var request checkinAccountInput
	if !bindCheckinJSON(c, &request, false, 1<<20) {
		return
	}
	if !request.SiteID.Present || request.SiteID.Value <= 0 || request.CredentialID.Present == (request.Credential != nil) || request.CredentialID.Present && request.CredentialID.Value <= 0 {
		resp.InvalidParam(c)
		return
	}
	account := model.CheckinAccount{SiteID: request.SiteID.Value, Enabled: true}
	model.CheckinAccountPatch(request.checkinAccountFields).Apply(&account)
	var credential *model.CheckinCredential
	if request.Credential != nil {
		value := request.Credential.Credential()
		credential = &value
	}
	created, err := a.services.Store.CreateAccount(c.Request.Context(), account, credential)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, checkinAccountDTO(created))
}

func (a *checkinAPI) patchAccount(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok {
		return
	}
	if _, ok := checkinQuery(c); !ok {
		return
	}
	var request model.CheckinAccountPatch
	if !bindCheckinJSON(c, &request, false, 1<<20) {
		return
	}
	account, err := a.services.Store.PatchAccount(c.Request.Context(), id, request)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, checkinAccountDTO(account))
}

func (a *checkinAPI) deleteAccount(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok || !checkinEmptyAction(c) {
		return
	}
	if err := a.services.Store.DeleteAccount(c.Request.Context(), id); err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, gin.H{"id": id})
}

func (a *checkinAPI) listCredentials(c *gin.Context) {
	if _, ok := checkinQuery(c); !ok {
		return
	}
	items, err := a.services.Store.ListCredentialSummaries(c.Request.Context())
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, items)
}

func (a *checkinAPI) getCredential(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok {
		return
	}
	if _, ok := checkinQuery(c); !ok {
		return
	}
	a.credentialSummary(c, id)
}

func (a *checkinAPI) credentialSummary(c *gin.Context, id int) {
	summary, err := a.services.Store.GetCredentialSummary(c.Request.Context(), id)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, summary)
}

func (a *checkinAPI) createCredential(c *gin.Context) {
	if _, ok := checkinQuery(c); !ok {
		return
	}
	var request model.CheckinCredentialInput
	if !bindCheckinJSON(c, &request, false, 1<<20) {
		return
	}
	credential, err := a.services.Store.CreateCredential(c.Request.Context(), request.Credential())
	if err != nil {
		checkinError(c, err)
		return
	}
	a.credentialSummary(c, credential.ID)
}

func (a *checkinAPI) patchCredential(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok {
		return
	}
	if _, ok := checkinQuery(c); !ok {
		return
	}
	var request model.CheckinCredentialPatch
	if !bindCheckinJSON(c, &request, false, 1<<20) {
		return
	}
	credential, err := a.services.Store.PatchCredential(c.Request.Context(), id, request)
	if err != nil {
		checkinError(c, err)
		return
	}
	a.credentialSummary(c, credential.ID)
}

func (a *checkinAPI) deleteCredential(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok || !checkinEmptyAction(c) {
		return
	}
	if err := a.services.Store.DeleteCredential(c.Request.Context(), id); err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, gin.H{"id": id})
}
