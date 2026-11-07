package balancer

import (
	"sort"
	"sync"

	"github.com/suprelory/octopus-plus/internal/model"
)

// Balancer 根据负载均衡模式选择通道
type Balancer interface {
	// Candidates 返回按策略排序的候选列表
	// 调用方在遍历候选列表时自行检查熔断状态
	Candidates(items []model.GroupItem) []model.GroupItem
}

// GetBalancer 根据模式返回对应的负载均衡器
func GetBalancer(mode model.GroupMode) Balancer {
	return getBalancer(mode, balanceScope{})
}

func getBalancer(mode model.GroupMode, scope balanceScope) Balancer {
	switch mode {
	case model.GroupModeRoundRobin:
		return &RoundRobin{scope: scope}
	case model.GroupModeFailover:
		return &Failover{}
	case model.GroupModeWeighted:
		return &Weighted{scope: scope}
	default:
		return &RoundRobin{scope: scope}
	}
}

// RoundRobin 轮询：从上次位置开始轮转排列
type RoundRobin struct {
	scope balanceScope
}

func (b *RoundRobin) Candidates(items []model.GroupItem) []model.GroupItem {
	return globalStrategyState.roundRobin(b.scope, items)
}

// Failover 故障转移：按优先级排序
type Failover struct{}

func (b *Failover) Candidates(items []model.GroupItem) []model.GroupItem {
	if len(items) == 0 {
		return nil
	}
	return sortByPriority(items)
}

// Weighted 加权分配：优先选择历史使用量与权重之比最低的候选
type Weighted struct {
	scope balanceScope
}

func (b *Weighted) Candidates(items []model.GroupItem) []model.GroupItem {
	return globalStrategyState.weighted(b.scope, items)
}

func sortByPriority(items []model.GroupItem) []model.GroupItem {
	sorted := make([]model.GroupItem, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		if sorted[i].ID != sorted[j].ID {
			return sorted[i].ID < sorted[j].ID
		}
		if sorted[i].ChannelID != sorted[j].ChannelID {
			return sorted[i].ChannelID < sorted[j].ChannelID
		}
		return sorted[i].ModelName < sorted[j].ModelName
	})
	return sorted
}

// Reset clears in-memory balancer state for tests.
func Reset() {
	globalStrategyState.reset()
	globalBreaker = sync.Map{}
	channelAffinity = sync.Map{}
	ResetKeyReservations()
	globalChannelHealth.reset()
}
