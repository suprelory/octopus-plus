package model

// A completion is inserted in the outcome transaction. ReadyAt lets the owner
// finish a bounded balance refresh before delivery; a crashed owner cannot lose
// the event. Only safe persisted outcome fields are included.
type CheckinCompletion struct {
	ID           string              `gorm:"primaryKey;size:96"`
	LogID        int64               `gorm:"not null;index"`
	Status       SiteExecutionStatus `gorm:"size:16;not null"`
	Entry        CheckinLog          `gorm:"type:text;serializer:json"`
	FailureCount int
	Balance      *float64
	ReadyAt      int64 `gorm:"not null;index"`
	Expanded     bool  `gorm:"not null;index"`
}

// Destination contains only the channel fingerprint, never a webhook, token or
// SMTP password. Delivery uses the currently configured matching destination.
type CheckinNotificationDelivery struct {
	ID           int64   `json:"id,string" gorm:"primaryKey"`
	CompletionID string  `json:"completion_id" gorm:"size:96;not null;uniqueIndex:idx_checkin_delivery,priority:1"`
	Destination  string  `json:"destination" gorm:"size:64;not null;uniqueIndex:idx_checkin_delivery,priority:2"`
	CooldownKey  string  `json:"-" gorm:"size:64;not null;index"`
	Event        string  `json:"event" gorm:"size:64;not null"`
	Threshold    float64 `json:"threshold"`
	CooldownMS   int64   `json:"-"`
	State        string  `json:"state" gorm:"size:16;not null;index"`
	Attempts     int     `json:"attempts"`
	StartedAt    int64   `json:"started_at"`
	FinishedAt   int64   `json:"finished_at"`
}

type CheckinNotificationCooldown struct {
	Key   string `gorm:"primaryKey;size:64"`
	Until int64  `gorm:"not null"`
}

// The gate serializes only short database transitions, never network delivery.
type CheckinNotificationGate struct {
	ID int `gorm:"primaryKey;autoIncrement:false"`
}

type CheckinNotificationPage struct {
	Items        []CheckinNotificationDelivery `json:"items"`
	NextBeforeID int64                         `json:"next_before_id,string,omitempty"`
}
