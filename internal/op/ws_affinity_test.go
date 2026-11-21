package op

import (
	"testing"
	"time"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestWSResponseAffinityCleanup(t *testing.T) {
	ctx := setupOpTestDB(t)

	now := time.Now()
	records := []model.WSResponseAffinity{
		{APIKeyID: 1, GroupID: 1, RequestModel: "m", ResponseIDHash: "expired", ChannelID: 1, ChannelKeyID: 1, ExpiresAt: now.Add(-time.Second)},
		{APIKeyID: 1, GroupID: 1, RequestModel: "m", ResponseIDHash: "live", ChannelID: 1, ChannelKeyID: 1, ExpiresAt: now.Add(time.Hour)},
	}
	if err := dbpkg.GetDB().Create(&records).Error; err != nil {
		t.Fatalf("create affinity rows failed: %v", err)
	}
	deleted, err := WSResponseAffinityCleanup(ctx, now)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one expired row deleted, got %d", deleted)
	}
	var count int64
	if err := dbpkg.GetDB().Model(&model.WSResponseAffinity{}).Count(&count).Error; err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one live row to remain, got %d", count)
	}
}
