package checkin

import "github.com/suprelory/octopus-plus/internal/op"

// Services shares session coordination and execution ownership across API and
// scheduled work. The caller prepares the schema and chooses runtime cutover;
// construction never migrates data, registers tasks or sends notifications.
type Services struct {
	Store         *op.CheckinStore
	Sessions      *Sessions
	Engine        *Engine
	Balances      *Balances
	Jobs          *Jobs
	Notifications *Notifications
}

func NewServices(store *op.CheckinStore, config func() NotificationConfig) *Services {
	if config == nil {
		config = func() NotificationConfig { return NotificationConfig{} }
	}
	sessions := NewSessions(store, NewHTTPAuthenticator(store))
	engine := NewEngine(store, sessions, store)
	notifications := NewNotifications(store, config)
	engine.WithNotifications(notifications)
	return &Services{Store: store, Sessions: sessions, Engine: engine, Balances: engine.balances, Jobs: NewJobs(store, engine), Notifications: notifications}
}
