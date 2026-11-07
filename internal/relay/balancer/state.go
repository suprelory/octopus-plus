package balancer

import "github.com/suprelory/octopus-plus/internal/op"

func init() {
	op.RegisterRelayBalancerStateReset(ResetStateByChannel)
	op.RegisterRelayBalancerGroupStateReset(ResetStateByGroup)
}

func ResetStateByChannel(channelID int) {
	resetCircuitBreakerByChannel(channelID)
	resetChannelAffinityByChannel(channelID)
	globalStrategyState.resetByChannel(channelID)
	globalChannelHealth.resetByChannel(channelID)
}

func ResetStateByGroup(groupID int) {
	resetChannelAffinityByGroup(groupID)
	globalStrategyState.resetByGroup(groupID)
}
