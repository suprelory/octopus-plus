package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func bestEffortWarmupUpstreamWS(
	ctx context.Context,
	apiKeyID int,
	supportedModels string,
	reqBody map[string]json.RawMessage,
) error {
	requestModel := strings.TrimSpace(extractWSRequestModel(reqBody))
	if requestModel == "" {
		return fmt.Errorf("warmup request missing model")
	}

	if supportedModels != "" {
		supportedModelsArray := strings.Split(supportedModels, ",")
		found := false
		for _, modelName := range supportedModelsArray {
			if modelName == requestModel {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("model not supported")
		}
	}

	group, err := op.GroupGetEnabledMap(requestModel, ctx)
	if err != nil {
		return fmt.Errorf("model not found")
	}
	candidateSnapshot := newCandidateSnapshot(ctx, group)

	iter := balancer.NewIterator(group, apiKeyID, requestModel)
	if iter.Len() == 0 {
		return fmt.Errorf("no available channel")
	}
	defer iter.Close()

	var lastErr error
	var lastCapabilityErr error
	var sawSupportedCapability bool
	capabilityPolicy := getCapabilityDegradationPolicy()
	for iter.Next() {
		item := iter.Item()

		channel, err := candidateSnapshot.Channel(item.ChannelID)
		if err != nil {
			lastErr = err
			continue
		}
		if !channel.Enabled {
			continue
		}

		decision := outbound.PlanRelayOperation(channel.Type, outbound.RelayOperationResponsesWebSocket)
		logRelayCapability(channel, item.ModelName, decision, capabilityPolicy)
		if reject, _ := evaluateCapabilityPolicy(decision, capabilityPolicy); reject {
			message := capabilityRejectionMessage(decision, channel.Type.String())
			iter.SkipWithCapability(channel.ID, 0, channel.Name, message, capabilityTrace(decision, capabilityPolicy, channel.Type.String()))
			lastCapabilityErr = fmt.Errorf("capability rejected: %s", message)
			continue
		}
		sawSupportedCapability = true
		excludedKeyIDs := make(map[int]struct{})

		for {
			usedKey, releaseKey := selectAndReserveRelayKey(iter, channel, excludedKeyIDs)
			if usedKey.ChannelKey == "" {
				break
			}

			if err := warmupUpstreamWSConnection(ctx, channel, usedKey); err != nil {
				releaseKey()
				lastErr = err
				excludedKeyIDs[usedKey.ID] = struct{}{}
				continue
			}

			releaseKey()
			return nil
		}
	}

	if lastErr != nil {
		return lastErr
	}
	if !sawSupportedCapability && lastCapabilityErr != nil {
		return lastCapabilityErr
	}
	return fmt.Errorf("no ws-capable channel available for warmup")
}

func warmupUpstreamWSConnection(ctx context.Context, channel *dbmodel.Channel, usedKey dbmodel.ChannelKey) error {
	warmupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	pc := TryUpstreamWS(warmupCtx, channel, channel.GetBaseUrl(), usedKey.ChannelKey, usedKey.ID, nil)
	if pc == nil {
		return fmt.Errorf("upstream ws unavailable")
	}

	wsUpstreamPool.Put(pc)
	return nil
}
