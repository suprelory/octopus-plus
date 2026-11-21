package checkin

import (
	"context"

	"github.com/suprelory/octopus-plus/internal/op"
)

// Services shares session coordination and execution ownership across API and
// scheduled work. The caller prepares the schema and injects notification config;
// construction never migrates data, registers tasks or sends notifications.
type Services struct {
	Store         *op.CheckinStore
	Sessions      *Sessions
	Engine        *Engine
	Balances      *Balances
	Jobs          *Jobs
	Notifications *Notifications
	workers       *workLifecycle
}

func NewServices(store *op.CheckinStore, config func() NotificationConfig) *Services {
	if config == nil {
		config = func() NotificationConfig { return NotificationConfig{} }
	}
	sessions := NewSessions(store, NewHTTPAuthenticator(store))
	engine := NewEngine(store, sessions, store)
	notifications := NewNotifications(store, config)
	engine.WithNotifications(notifications)
	jobs := NewJobs(store, engine)
	workers := newWorkLifecycle()
	sessions.workers, engine.workers, notifications.workers, jobs.workers = workers, workers, workers, workers
	engine.balances.workers = workers
	return &Services{Store: store, Sessions: sessions, Engine: engine, Balances: engine.balances, Jobs: jobs, Notifications: notifications, workers: workers}
}

// Begin also covers API configuration requests during HTTP shutdown.
func (s *Services) Begin(ctx context.Context) (context.Context, func(), error) {
	return s.workers.begin(ctx)
}

func (s *Services) Close(ctx context.Context) error { return s.workers.close(ctx) }
