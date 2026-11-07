package balancer

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

const defaultChannelAffinityTTL = time.Hour

// Scope is an opaque digest. Raw session identifiers never enter routing logs.
type AffinityOptions struct {
	Mode   string `json:"mode"`
	Scope  string `json:"-"`
	Source string `json:"source"`
}

func affinityOptions(options []AffinityOptions) AffinityOptions {
	if len(options) > 0 {
		return options[0]
	}
	return AffinityOptions{Mode: "prefer", Source: "api_key"}
}

func affinityKey(apiKeyID, groupID int, requestModel string, options AffinityOptions) string {
	key := sessionKey(apiKeyID, groupID, requestModel)
	if options.Scope != "" {
		return fmt.Sprintf("session:%d:%d:%d:%s:%s", apiKeyID, groupID, len(requestModel), requestModel, options.Scope)
	}
	return key
}

type SessionEntry struct {
	GroupID      int
	ChannelID    int
	ChannelKeyID int
	Timestamp    time.Time
}

func sessionKey(apiKeyID, groupID int, requestModel string) string {
	return fmt.Sprintf("%d:%d:%s", apiKeyID, groupID, requestModel)
}

// channelAffinity stores the channel/key that completed the latest real model
// request. The scope is API key + group + request model. It is process-local
// and uses the global affinity TTL.
var channelAffinity sync.Map // key: apiKeyID:groupID:requestModel -> *SessionEntry
var affinityWrites atomic.Uint64

func channelAffinityEnabled() bool {
	enabled, err := op.SettingGetBool(model.SettingKeyChannelAffinityEnabled)
	if err != nil {
		return true
	}
	return enabled
}

func channelAffinityTTL() time.Duration {
	seconds, err := op.SettingGetInt(model.SettingKeyChannelAffinityTTLSeconds)
	if err != nil || seconds < 1 {
		return defaultChannelAffinityTTL
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl <= 0 {
		return defaultChannelAffinityTTL
	}
	return ttl
}

// GetChannelAffinity reads an unexpired affinity while the switch is enabled.
func GetChannelAffinity(apiKeyID, groupID int, requestModel string, options ...AffinityOptions) *SessionEntry {
	return getChannelAffinity(apiKeyID, groupID, requestModel, affinityOptions(options), false)
}

func getChannelAffinity(apiKeyID, groupID int, requestModel string, options AffinityOptions, readOnly bool) *SessionEntry {
	if !channelAffinityEnabled() || options.Mode == "off" {
		return nil
	}
	key := affinityKey(apiKeyID, groupID, requestModel, options)
	value, ok := channelAffinity.Load(key)
	if !ok {
		return nil
	}
	entry, ok := value.(*SessionEntry)
	if !ok || entry == nil {
		if !readOnly {
			channelAffinity.Delete(key)
		}
		return nil
	}
	if time.Since(entry.Timestamp) > channelAffinityTTL() {
		if !readOnly {
			channelAffinity.Delete(key)
		}
		return nil
	}
	cloned := *entry
	return &cloned
}

// SetChannelAffinity records a fully successful real request. Disabled affinity
// neither writes nor refreshes entries, and existing entries remain in memory.
func SetChannelAffinity(apiKeyID, groupID int, requestModel string, channelID, keyID int, options ...AffinityOptions) {
	option := affinityOptions(options)
	if !channelAffinityEnabled() || channelID <= 0 || option.Mode == "off" {
		return
	}
	cacheKey := affinityKey(apiKeyID, groupID, requestModel, option)
	entry := &SessionEntry{
		GroupID:      groupID,
		ChannelID:    channelID,
		ChannelKeyID: keyID,
		Timestamp:    time.Now(),
	}
	if option.Mode == "strict" {
		for {
			previous, loaded := channelAffinity.LoadOrStore(cacheKey, entry)
			if !loaded {
				break
			}
			old, ok := previous.(*SessionEntry)
			if ok && old != nil && time.Since(old.Timestamp) <= channelAffinityTTL() && old.ChannelID != channelID {
				return
			}
			if channelAffinity.CompareAndSwap(cacheKey, previous, entry) {
				break
			}
		}
	} else {
		channelAffinity.Store(cacheKey, entry)
	}
	if affinityWrites.Add(1)%256 == 0 {
		pruneChannelAffinity()
	}
}

// Periodic pruning bounds session-cardinality growth without scanning on reads.
func pruneChannelAffinity() {
	const maxEntries = 16384
	ttl, count := channelAffinityTTL(), 0
	channelAffinity.Range(func(key, value any) bool {
		entry, ok := value.(*SessionEntry)
		if !ok || entry == nil || time.Since(entry.Timestamp) > ttl {
			channelAffinity.CompareAndDelete(key, value)
			return true
		}
		count++
		if count > maxEntries {
			channelAffinity.CompareAndDelete(key, value)
		}
		return true
	})
}

func DeleteChannelAffinity(apiKeyID, groupID int, requestModel string, options ...AffinityOptions) {
	channelAffinity.Delete(affinityKey(apiKeyID, groupID, requestModel, affinityOptions(options)))
}

// SetRoutingAffinity records the latest fully successful route.
func SetRoutingAffinity(apiKeyID, groupID int, requestModel string, channelID, keyID int, options ...AffinityOptions) {
	SetChannelAffinity(apiKeyID, groupID, requestModel, channelID, keyID, options...)
}

// DeleteRoutingAffinity does not touch Responses replay/previous_response_id.
func DeleteRoutingAffinity(apiKeyID, groupID int, requestModel string, options ...AffinityOptions) {
	DeleteChannelAffinity(apiKeyID, groupID, requestModel, options...)
}

func resetChannelAffinityByChannel(channelID int) {
	channelAffinity.Range(func(key, value any) bool {
		entry, ok := value.(*SessionEntry)
		if ok && entry != nil && entry.ChannelID == channelID {
			channelAffinity.Delete(key)
		}
		return true
	})
}

func resetChannelAffinityByGroup(groupID int) {
	channelAffinity.Range(func(key, value any) bool {
		entry, ok := value.(*SessionEntry)
		if ok && entry != nil && entry.GroupID == groupID {
			channelAffinity.Delete(key)
		}
		return true
	})
}
