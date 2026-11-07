package op

import (
	"errors"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func TestStatsSaveTaskPersistsChannelKeyRuntime(t *testing.T) {
	for _, failStats := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "stats-write-failure"}[failStats], func(t *testing.T) {
			ctx := setupChannelSupportTestDB(t)
			if err := InitCache(); err != nil {
				t.Fatal(err)
			}
			channel := createLifecycleChannel(t, ctx)
			key := channel.Keys[0]
			key.StatusCode, key.LastUseTimeStamp = 201, 123
			if err := ChannelKeyUpdateWithDelta(key, 7.25); err != nil {
				t.Fatal(err)
			}
			statsFailureObserved := false
			if failStats {
				conn := db.GetDB()
				fail := func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "StatsTotal" {
						statsFailureObserved = true
						tx.AddError(errors.New("synthetic stats failure"))
					}
				}
				if err := conn.Callback().Update().Before("gorm:update").Register("test:stats_failure", fail); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Callback().Update().Remove("test:stats_failure") })
			}
			StatsSaveDBTask()
			if failStats && !statsFailureObserved {
				t.Fatal("statistics failure path was not exercised")
			}
			var saved model.ChannelKey
			if err := db.GetDB().First(&saved, key.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.TotalCost != 7.25 || saved.StatusCode != 201 || saved.LastUseTimeStamp != 123 {
				t.Fatalf("periodic task did not persist key runtime: %+v", saved)
			}
		})
	}
}
