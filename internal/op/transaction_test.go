package op

import (
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

func TestGroupUpdatePanicRollsBackAndReturnsFailure(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	group := &model.Group{Name: "before-panic", Mode: model.GroupModeRoundRobin}
	if err := GroupCreate(group, ctx); err != nil {
		t.Fatal(err)
	}
	core, entries := observer.New(zap.ErrorLevel)
	previous := log.Logger
	log.Logger = zap.New(core).Sugar()
	t.Cleanup(func() { log.Logger = previous })
	callbackName := "test:panic_after_group_update"
	if err := db.GetDB().Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "groups" {
			panic("failed password=panic-secret")
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.GetDB().Callback().Update().Remove(callbackName) })
	name := "after-panic"
	updated, err := GroupUpdate(&model.GroupUpdateRequest{ID: group.ID, Name: &name}, ctx)
	if err == nil || updated != nil {
		t.Fatalf("panic reported success: %+v, %v", updated, err)
	}
	var saved model.Group
	if err := db.GetDB().First(&saved, group.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Name != "before-panic" {
		t.Fatalf("transaction was not rolled back: %s", saved.Name)
	}
	if entries.Len() != 1 {
		t.Fatalf("panic logs=%d", entries.Len())
	}
	fields := entries.All()[0].ContextMap()
	if fields["stack"] == "" || strings.Contains(fields["panic"].(string), "panic-secret") {
		t.Fatalf("unsafe or missing panic diagnostic: %+v", fields)
	}
}
