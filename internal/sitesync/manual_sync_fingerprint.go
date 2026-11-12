package sitesync

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/suprelory/octopus-plus/internal/model"
)

var manualSyncFingerprintKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err == nil {
		return key
	}
	fallback := sha256.Sum256([]byte("octopus-manual-sync-preview"))
	return fallback[:]
}()

func buildManualSyncFingerprint(accountID int, mode string, format string, sections manualSyncSections, credentialVersions ...int64) string {
	tokens := cloneSiteTokens(sections.tokens)
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].GroupKey != tokens[j].GroupKey {
			return tokens[i].GroupKey < tokens[j].GroupKey
		}
		if tokens[i].Name != tokens[j].Name {
			return tokens[i].Name < tokens[j].Name
		}
		return tokens[i].Token < tokens[j].Token
	})
	groups := cloneSiteGroups(sections.groups)
	sort.Slice(groups, func(i, j int) bool { return groups[i].GroupKey < groups[j].GroupKey })
	models := make([]model.SiteModel, 0)
	for _, items := range sections.models {
		models = append(models, cloneSiteModels(items)...)
	}
	sortSiteModels(models)
	payload := struct {
		AccountID          int                   `json:"account_id"`
		Mode               string                `json:"mode"`
		Format             string                `json:"format"`
		TokensProvided     bool                  `json:"tokens_provided"`
		GroupsProvided     bool                  `json:"groups_provided"`
		Tokens             []model.SiteToken     `json:"tokens"`
		Groups             []model.SiteUserGroup `json:"groups"`
		Models             []model.SiteModel     `json:"models"`
		Balance            *float64              `json:"balance"`
		BalanceUsed        *float64              `json:"balance_used"`
		TodayIncome        *float64              `json:"today_income"`
		AccessToken        *string               `json:"access_token"`
		CredentialVersions []int64               `json:"credential_versions,omitempty"`
	}{accountID, mode, format, sections.tokensProvided, sections.groupsProvided, tokens, groups, models, sections.balance, sections.balanceUsed, sections.todayIncome, sections.accessToken, credentialVersions}
	encoded, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, manualSyncFingerprintKey)
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil))
}
