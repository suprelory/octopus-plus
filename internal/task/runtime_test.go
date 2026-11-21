package task

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/backup"
	"github.com/suprelory/octopus-plus/internal/checkin"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestRuntimeTasksUseIndependentServicesAndDefaultBackup(t *testing.T) {
	resetTaskScheduler(t)
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "tasks.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	var submissions, notifications atomic.Int32
	uploaded := make(chan int, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/submit":
			submissions.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		case r.URL.Path == "/notify":
			notifications.Add(1)
			w.WriteHeader(204)
		case r.Method == "PUT":
			var document backup.Document
			if err := json.NewDecoder(r.Body).Decode(&document); err != nil {
				t.Error(err)
			}
			uploaded <- document.Version
			w.WriteHeader(201)
		case r.Method == "PROPFIND":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/backups/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		default:
			w.WriteHeader(201)
		}
	}))
	defer upstream.Close()
	for key, value := range map[model.SettingKey]string{
		model.SettingKeyModelInfoUpdateInterval: "0", model.SettingKeySyncLLMInterval: "0", model.SettingKeyWebDAVURL: upstream.URL,
		model.SettingKeyWebDAVBackupPath: "/backups", model.SettingKeyWebDAVRetentionCount: "0", model.SettingKeyProxyURL: "",
		model.SettingKeyCheckinNotifyEnabled: "true", model.SettingKeyCheckinNotifySuccessEnabled: "true", model.SettingKeyCheckinNotifyManualEnabled: "true",
		model.SettingKeyCheckinNotifyWebhookURL: upstream.URL + "/notify",
	} {
		if err := op.SettingSetString(key, value); err != nil {
			t.Fatal(err)
		}
	}
	services := checkin.NewServices(op.NewCheckinStore(db.GetDB()), checkin.LoadNotificationConfig)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := Stop(ctx); err != nil {
			t.Error(err)
		}
		if err := services.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	site, err := services.Store.CreateSite(t.Context(), model.CheckinSite{Name: "task-site", Platform: model.SitePlatformAPI, BaseURL: upstream.URL, Enabled: true, CheckinHTTPEnabled: true, CheckinHTTPPath: "/submit", CheckinTimezone: "UTC", CheckinWindowStart: "00:00", CheckinWindowEnd: "23:59"})
	if err != nil {
		t.Fatal(err)
	}
	account, err := services.Store.CreateAccount(t.Context(), model.CheckinAccount{SiteID: site.ID, Name: "task-account", Enabled: true, AutoCheckin: true, AuthMode: model.CheckinAuthCookie}, &model.CheckinCredential{Name: "task-cookie", Platform: model.SitePlatformAPI, Type: model.CheckinCredentialCookie, AuthBaseURL: upstream.URL, Cookie: "session=task-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.Engine.Execute(t.Context(), account.ID, model.CheckinTriggerManual); err != nil {
		t.Fatal(err)
	}
	if err := services.Notifications.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if submissions.Load() != 1 || notifications.Load() != 1 {
		t.Fatal("production notification settings were not applied")
	}
	Init(services)
	tasksMu.RLock()
	_, legacy := tasks[string(model.SettingKeySiteSyncInterval)]
	scan := tasks[TaskSiteCheckin].fn
	notificationsTask := tasks[TaskCheckinNotifications].fn
	backupTask := tasks[string(model.SettingKeyWebDAVBackupInterval)].fn
	tasksMu.RUnlock()
	if legacy {
		t.Fatal("legacy sync task was registered")
	}
	scan()
	notificationsTask()
	if err := services.Notifications.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if submissions.Load() != 1 || notifications.Load() != 1 {
		t.Fatal("schedule/notification recovery repeated a completed operation")
	}
	backupTask()
	select {
	case version := <-uploaded:
		if version != 2 {
			t.Fatal("scheduled backup selected legacy format")
		}
	case <-time.After(time.Second):
		t.Fatal("scheduled backup was not uploaded")
	}
	if err := op.SettingSetString(model.SettingKeySiteSyncInterval, "1"); err == nil {
		t.Fatal("old settings can reactivate synchronization")
	}
}

func TestRuntimeSchedulerStopCancelsAndDrainsWorkers(t *testing.T) {
	resetTaskScheduler(t)
	runtimeContext, stopRuntime = context.WithCancel(context.Background())
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	Register("blocked", time.Hour, true, func() { close(started); <-taskContext().Done(); <-release; close(finished) })
	RUN()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop did not wait for persistence: %v", err)
	}
	if err := Configure("new", time.Second, true, func() {}); err == nil {
		t.Fatal("shutdown accepted new work")
	}
	if err := Update("blocked", time.Second); err == nil {
		t.Fatal("shutdown accepted a schedule update")
	}
	close(release)
	if err := Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("shutdown returned before worker ended")
	}
}
