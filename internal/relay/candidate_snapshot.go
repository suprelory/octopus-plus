package relay

import (
	"context"
	"fmt"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

// candidateSnapshot freezes the channel configuration used by one request.
// Group resolution and channel lookup happen once; retries then observe a
// consistent candidate set even if the admin cache is refreshed mid-request.
type candidateSnapshot struct {
	group    dbmodel.Group
	channels map[int]*dbmodel.Channel
	errors   map[int]error
}

func newCandidateSnapshot(ctx context.Context, group dbmodel.Group) *candidateSnapshot {
	snapshot := &candidateSnapshot{
		group:    group,
		channels: make(map[int]*dbmodel.Channel),
		errors:   make(map[int]error),
	}
	for _, item := range group.Items {
		if _, seen := snapshot.channels[item.ChannelID]; seen {
			continue
		}
		if _, seen := snapshot.errors[item.ChannelID]; seen {
			continue
		}
		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err != nil {
			snapshot.errors[item.ChannelID] = err
			continue
		}
		snapshot.channels[item.ChannelID] = channel
	}
	return snapshot
}

func (s *candidateSnapshot) Channel(id int) (*dbmodel.Channel, error) {
	if s == nil {
		return nil, fmt.Errorf("candidate snapshot is nil")
	}
	if channel, ok := s.channels[id]; ok {
		return channel, nil
	}
	if err, ok := s.errors[id]; ok {
		return nil, err
	}
	return nil, fmt.Errorf("channel %d not found in candidate snapshot", id)
}
