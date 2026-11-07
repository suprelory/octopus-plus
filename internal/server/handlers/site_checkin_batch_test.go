package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestManualFullCheckinReturnsAndQueriesBatchTask(t *testing.T) {
	ctx := setupSiteHandlerTestDB(t)
	w, response := requestSiteMutation(t, "/api/v1/site/checkin-all", checkinAllSiteAccounts, map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("start batch returned %d: %s", w.Code, w.Body.String())
	}
	data, ok := response.Data.(map[string]any)
	if !ok || data["id"] == "" || data["status"] == "" {
		t.Fatalf("task id and status were not returned: %s", w.Body.String())
	}
	taskID, err := strconv.ParseInt(data["id"].(string), 10, 64)
	if err != nil || taskID <= 0 {
		t.Fatalf("invalid task id: %#v", data["id"])
	}

	deadline := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	for {
		job, err := querySiteCheckinBatch(t, strconv.FormatInt(taskID, 10), getSiteCheckinBatch)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != model.SiteCheckinBatchJobStatusQueued && job.Status != model.SiteCheckinBatchJobStatusRunning {
			if job.Status != model.SiteCheckinBatchJobStatusCompleted || job.Total != 0 {
				t.Fatalf("unexpected empty task result: %+v", job)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("task query timed out")
		case <-deadline.C:
		}
	}
	latest, err := querySiteCheckinBatch(t, "", getLatestSiteCheckinBatch)
	if err != nil || latest.ID != taskID {
		t.Fatalf("latest task query returned %+v, %v", latest, err)
	}
}

func querySiteCheckinBatch(t *testing.T, taskID string, handler gin.HandlerFunc) (*model.SiteCheckinBatchJob, error) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	path := "/api/v1/site/checkin-batches/latest"
	if taskID != "" {
		path = "/api/v1/site/checkin-batches/" + taskID
		c.Params = gin.Params{{Key: "id", Value: taskID}}
	}
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	handler(c)
	if w.Code != http.StatusOK {
		return nil, strconv.ErrSyntax
	}
	var response struct {
		Data model.SiteCheckinBatchJob `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		return nil, err
	}
	return &response.Data, nil
}

func TestSelectedCheckinBatchRespectsScopeAndReusesMatchingTask(t *testing.T) {
	ctx := setupSiteHandlerTestDB(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var callsMu sync.Mutex
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		token := r.Header.Get("Authorization")
		callsMu.Lock()
		calls = append(calls, token)
		callsMu.Unlock()
		if token == "Bearer selected-a" {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if token == "Bearer selected-b" {
			_, _ = w.Write([]byte(`{"success":false,"message":"test rejection"}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"reward":1}}`))
	}))
	defer upstream.Close()
	defer unblock()
	var sites []*model.Site
	for _, name := range []string{"Selected A", "Selected B", "Unselected"} {
		site := &model.Site{Name: name, Kind: model.SiteKindCheckin, Platform: model.SitePlatformOneAPI, BaseURL: upstream.URL, Enabled: true}
		if err := op.SiteCreate(site, ctx); err != nil {
			t.Fatal(err)
		}
		sites = append(sites, site)
	}
	createAccount := func(site *model.Site, token string, enabled, auto bool) *model.SiteAccount {
		account := &model.SiteAccount{
			SiteID: site.ID, Name: token, CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: token,
			Enabled: enabled, EnabledSet: true, AutoCheckin: auto, AutoCheckinSet: true,
		}
		if err := op.SiteAccountCreate(account, ctx); err != nil {
			t.Fatal(err)
		}
		return account
	}
	first := createAccount(sites[0], "selected-a", true, true)
	second := createAccount(sites[1], "selected-b", true, true)
	createAccount(sites[1], "manual-only", true, false)
	createAccount(sites[1], "disabled", false, true)
	createAccount(sites[2], "unselected", true, true)

	w, _ := requestSiteMutation(t, "/api/v1/site/batch", batchSite, map[string]any{
		"action": "checkin", "ids": []int{sites[1].ID, sites[0].ID, sites[0].ID},
	})
	var response struct {
		Data model.SiteCheckinBatchJob `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	job := response.Data
	if w.Code != http.StatusOK || job.ID == 0 || !slices.Equal(job.SiteIDs, []int{sites[0].ID, sites[1].ID}) {
		t.Fatalf("selected batch did not return its task and scope: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("selected batch did not reach the upstream")
	}
	duplicate, _ := requestSiteMutation(t, "/api/v1/site/batch", batchSite, map[string]any{
		"action": "checkin", "ids": []int{sites[0].ID, sites[1].ID},
	})
	if err := json.Unmarshal(duplicate.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if duplicate.Code != http.StatusOK || response.Data.ID != job.ID {
		t.Fatalf("matching selection did not reuse the task: %s", duplicate.Body.String())
	}
	conflict, _ := requestSiteMutation(t, "/api/v1/site/batch", batchSite, map[string]any{
		"action": "checkin", "ids": []int{sites[2].ID},
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("different selection reused the wrong task: %d %s", conflict.Code, conflict.Body.String())
	}
	full, _ := requestSiteMutation(t, "/api/v1/site/checkin-all", checkinAllSiteAccounts, map[string]any{})
	if full.Code != http.StatusConflict {
		t.Fatalf("full checkin reused a selected task: %d %s", full.Code, full.Body.String())
	}
	unblock()
	finished := waitForSiteCheckinBatch(t, job.ID)
	if finished.Status != model.SiteCheckinBatchJobStatusCompletedWithErrors || finished.Total != 2 || finished.Attempted != 2 || finished.Success != 1 || finished.Failed != 1 || !slices.Equal(finished.SiteIDs, job.SiteIDs) {
		t.Fatalf("incorrect selected batch outcome: %+v", finished)
	}
	callsMu.Lock()
	actualCalls := slices.Clone(calls)
	callsMu.Unlock()
	slices.Sort(actualCalls)
	if !slices.Equal(actualCalls, []string{"Bearer selected-a", "Bearer selected-b"}) {
		t.Fatalf("wrong accounts or duplicate requests: %v", actualCalls)
	}
	logs, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{BatchJobID: job.ID})
	if err != nil || len(logs.Items) != 2 {
		t.Fatalf("selected batch logs missing: %+v, %v", logs, err)
	}
	for _, entry := range logs.Items {
		if entry.AccountID != first.ID && entry.AccountID != second.ID {
			t.Fatalf("batch logged an unselected account: %+v", entry)
		}
	}
	var count int64
	if err := db.GetDB().Model(&model.SiteCheckinBatchJob{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate/conflicting requests created tasks: count=%d err=%v", count, err)
	}
}

func TestSelectedCheckinBatchRejectsInvalidScopeWithoutStartingWork(t *testing.T) {
	ctx := setupSiteHandlerTestDB(t)
	var sites []*model.Site
	for _, kind := range []model.SiteKind{model.SiteKindRelay, model.SiteKindCheckin, model.SiteKindCheckin} {
		site := &model.Site{Name: "Scope " + strconv.Itoa(len(sites)), Kind: kind, Platform: model.SitePlatformOneAPI, BaseURL: "https://unused.example", Enabled: true}
		if err := op.SiteCreate(site, ctx); err != nil {
			t.Fatal(err)
		}
		sites = append(sites, site)
	}
	if err := db.GetDB().Model(sites[1]).Update("archived", true).Error; err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]int{nil, {}, {0}, {-1}, {99999}, {sites[0].ID}, {sites[1].ID}, {sites[2].ID, 99999}} {
		w, _ := requestSiteMutation(t, "/api/v1/site/batch", batchSite, map[string]any{"action": "checkin", "ids": ids})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid scope %v was accepted: %d %s", ids, w.Code, w.Body.String())
		}
	}
	var count int64
	if err := db.GetDB().Model(&model.SiteCheckinBatchJob{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("invalid scope started a task: count=%d err=%v", count, err)
	}
}

func waitForSiteCheckinBatch(t *testing.T, taskID int64) *model.SiteCheckinBatchJob {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	for {
		job, err := querySiteCheckinBatch(t, strconv.FormatInt(taskID, 10), getSiteCheckinBatch)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != model.SiteCheckinBatchJobStatusQueued && job.Status != model.SiteCheckinBatchJobStatusRunning {
			return job
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("batch did not finish")
		}
	}
}
