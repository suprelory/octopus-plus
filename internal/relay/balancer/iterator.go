package balancer

import (
	"fmt"
	"sort"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

// Iterator 统一的负载均衡迭代器
// 内部编排：策略排序 + 粘性优先 + 决策追踪
type Iterator struct {
	candidates     []model.GroupItem
	tiers          []candidateTier
	index          int
	preferences    map[int]routingPreference
	apiKeyID       int
	groupID        int
	requestModel   string
	modelName      string // 请求模型名（用于熔断检查）
	mode           model.GroupMode
	affinity       AffinityOptions
	strictAffinity bool
	readOnly       bool

	// 内嵌追踪
	attempts []model.ChannelAttempt
	count    int

	currentReservation *channelHealthReservation
	selectionMetrics   model.ChannelSelectionMetrics
	healthBaseRanks    map[candidateIdentity]int
}

// QualityRanker returns a lower-is-better semantic conversion quality rank for
// a candidate. The iterator partitions candidates by rank, then applies the
// configured load-balancer independently inside each quality tier.
type QualityRanker func(model.GroupItem) int

// PreferenceSource identifies why a candidate was moved ahead of the normal
// load-balancer order. The numeric order also represents routing priority.
type PreferenceSource uint8

const (
	PreferenceNone PreferenceSource = iota
	PreferenceResponsesReplay
	PreferenceChannelAffinity
)

func (p PreferenceSource) String() string {
	switch p {
	case PreferenceResponsesReplay:
		return "responses_replay"
	case PreferenceChannelAffinity:
		return "channel_affinity"
	default:
		return "normal"
	}
}

type routingPreference struct {
	source      PreferenceSource
	entry       SessionEntry
	qualityRank int
}

type candidateTier struct {
	start      int
	end        int
	mode       model.GroupMode
	scope      balanceScope
	healthTopK int
	scheduled  bool
}

type CapabilityTrace struct {
	AdapterType      string
	Status           string
	Policy           string
	ConversionPath   []string
	Conversion       *model.RequestConversion
	RequiredFeatures []string
	DegradedFields   []string
	Losses           []model.CapabilityLoss
	Lossiness        string
	Reasons          []string
}

// NewIterator 创建负载均衡迭代器
// 自动处理：策略排序 + 粘性通道提前
func NewIterator(group model.Group, apiKeyID int, requestModel string, affinity ...AffinityOptions) *Iterator {
	return NewIteratorWithPreferenceAndQuality(group, apiKeyID, requestModel, nil, nil, affinity...)
}

// NewIteratorWithPreference 创建带优先通道偏好的负载均衡迭代器。
// preferred 非空时，会优先把指定通道提前到候选列表最前面。
func NewIteratorWithPreference(group model.Group, apiKeyID int, requestModel string, preferred *SessionEntry) *Iterator {
	return NewIteratorWithPreferenceAndQuality(group, apiKeyID, requestModel, preferred, nil)
}

// NewIteratorWithPreferenceAndQuality is the request-aware iterator constructor.
// Quality is evaluated before sticky preferences are applied; a sticky route
// therefore remains preferred only among candidates in the same quality tier.
func NewIteratorWithPreferenceAndQuality(group model.Group, apiKeyID int, requestModel string, preferred *SessionEntry, quality QualityRanker, options ...AffinityOptions) *Iterator {
	return newIterator(group, apiKeyID, requestModel, preferred, quality, affinityOptions(options), false)
}

// NewPreviewIterator observes the same ordering without changing scheduling,
// affinity, health reservations or half-open circuit probes.
func NewPreviewIterator(group model.Group, apiKeyID int, requestModel string, quality QualityRanker, options AffinityOptions) *Iterator {
	return newIterator(group, apiKeyID, requestModel, nil, quality, options, true)
}

func newIterator(group model.Group, apiKeyID int, requestModel string, preferred *SessionEntry, quality QualityRanker, option AffinityOptions, readOnly bool) *Iterator {
	bound := getChannelAffinity(apiKeyID, group.ID, requestModel, option, readOnly)
	strict := preferred == nil && option.Mode == "strict" && bound != nil
	if strict {
		items := make([]model.GroupItem, 0, len(group.Items))
		for _, item := range group.Items {
			if item.ChannelID == bound.ChannelID {
				items = append(items, item)
			}
		}
		group.Items = items
	}
	rankedTiers := partitionQualityTiers(group.Items, quality)
	preferenceCandidates := make([]routingPreference, 0, 2)
	if preferred != nil && preferred.ChannelID > 0 {
		preferenceCandidates = append(preferenceCandidates, routingPreference{
			source: PreferenceResponsesReplay,
			entry:  *preferred,
		})
	}
	if affinity := bound; affinity != nil {
		preferenceCandidates = append(preferenceCandidates, routingPreference{
			source: PreferenceChannelAffinity,
			entry:  *affinity,
		})
	}
	candidates := make([]model.GroupItem, 0, len(group.Items))
	preferences := make(map[int]routingPreference, len(preferenceCandidates))
	selectedChannels := make(map[int]struct{}, len(preferenceCandidates))
	for _, preference := range preferenceCandidates {
		channelID := preference.entry.ChannelID
		if channelID <= 0 {
			continue
		}
		if _, selected := selectedChannels[channelID]; selected {
			continue
		}

		preferredItem, preferredTier, found := findPreferredCandidate(rankedTiers, channelID)
		if !found {
			if preference.source == PreferenceChannelAffinity && !strict && !readOnly {
				DeleteChannelAffinity(apiKeyID, group.ID, requestModel, option)
			}
			continue
		}
		if preferredTier != 0 {
			continue
		}
		if preference.source == PreferenceChannelAffinity && !strict && healthCooldownActive(globalChannelHealth.snapshot(channelID, preferredItem.ModelName), time.Now()) {
			continue
		}

		preference.qualityRank = rankedTiers[preferredTier].rank
		preferences[len(candidates)] = preference
		candidates = append(candidates, preferredItem)
		selectedChannels[channelID] = struct{}{}

		// A group may contain multiple mappings for the same channel. Once a
		// preferred channel is selected, remove its duplicate fallback items.
		for tierIndex := range rankedTiers {
			filtered := rankedTiers[tierIndex].items[:0]
			for _, item := range rankedTiers[tierIndex].items {
				if item.ChannelID != channelID {
					filtered = append(filtered, item)
				}
			}
			rankedTiers[tierIndex].items = filtered
		}
	}

	tiers := make([]candidateTier, 0, len(rankedTiers))
	for _, rankedTier := range rankedTiers {
		if len(rankedTier.items) == 0 {
			continue
		}
		start := len(candidates)
		candidates = append(candidates, rankedTier.items...)
		tiers = append(tiers, candidateTier{
			start: start,
			end:   len(candidates),
			mode:  group.Mode,
			scope: balanceScope{
				groupID:       group.ID,
				requestModel:  requestModel,
				qualityTier:   rankedTier.rank,
				qualityRanked: quality != nil,
			},
			healthTopK: candidateHealthTopK(group.MaxRetries),
		})
	}

	return &Iterator{
		candidates:      candidates,
		tiers:           tiers,
		index:           -1,
		preferences:     preferences,
		apiKeyID:        apiKeyID,
		groupID:         group.ID,
		requestModel:    requestModel,
		modelName:       requestModel,
		mode:            group.Mode,
		affinity:        option,
		strictAffinity:  strict,
		readOnly:        readOnly,
		healthBaseRanks: make(map[candidateIdentity]int),
	}
}

func candidateHealthTopK(maxRetries int) int {
	if maxRetries < 3 {
		return 3
	}
	if maxRetries > 16 {
		return 16
	}
	return maxRetries
}

type rankedCandidateTier struct {
	rank  int
	items []model.GroupItem
}

func partitionQualityTiers(items []model.GroupItem, quality QualityRanker) []rankedCandidateTier {
	if len(items) == 0 {
		return nil
	}
	if quality == nil {
		return []rankedCandidateTier{{items: canonicalCandidates(items)}}
	}

	byRank := make(map[int][]model.GroupItem)
	ranks := make([]int, 0)
	for _, item := range items {
		rank := quality(item)
		if _, exists := byRank[rank]; !exists {
			ranks = append(ranks, rank)
		}
		byRank[rank] = append(byRank[rank], item)
	}
	sort.Ints(ranks)
	result := make([]rankedCandidateTier, 0, len(ranks))
	for _, rank := range ranks {
		result = append(result, rankedCandidateTier{rank: rank, items: canonicalCandidates(byRank[rank])})
	}
	return result
}

func findPreferredCandidate(tiers []rankedCandidateTier, channelID int) (model.GroupItem, int, bool) {
	for tierIndex, tier := range tiers {
		for _, item := range tier.items {
			if item.ChannelID == channelID {
				return item, tierIndex, true
			}
		}
	}
	return model.GroupItem{}, -1, false
}

// Next 移动到下一个候选，返回 false 表示遍历完成
func (it *Iterator) Next() bool {
	it.releaseCurrentReservation()
	next := it.index + 1
	if next >= len(it.candidates) {
		it.index = next
		return false
	}
	for tierIndex := range it.tiers {
		tier := &it.tiers[tierIndex]
		if tier.scheduled || tier.start != next {
			continue
		}
		baseCandidates := append([]model.GroupItem(nil), it.candidates[tier.start:tier.end]...)
		var ordered []model.GroupItem
		if it.readOnly {
			ordered = globalStrategyState.preview(tier.mode, tier.scope, baseCandidates)
		} else {
			ordered = getBalancer(tier.mode, tier.scope).Candidates(baseCandidates)
		}
		baseSelected := model.GroupItem{}
		if len(ordered) > 0 {
			baseSelected = ordered[0]
		}
		for baseRank, item := range ordered {
			it.healthBaseRanks[groupItemIdentity(item)] = baseRank
		}
		ordered = globalChannelHealth.rankCandidates(tier.mode, ordered, tier.healthTopK)
		if !it.readOnly && tier.mode == model.GroupModeWeighted && len(ordered) > 0 {
			globalStrategyState.adjustWeightedSelection(tier.scope, baseCandidates, baseSelected, ordered[0])
		}
		copy(it.candidates[tier.start:tier.end], ordered)
		tier.scheduled = true
		break
	}
	it.index = next
	snapshot := globalChannelHealth.snapshot(it.Item().ChannelID, it.Item().ModelName)
	it.selectionMetrics = it.selectionMetricsFromSnapshot(it.Item(), snapshot)
	return true
}

func groupItemIdentity(item model.GroupItem) candidateIdentity {
	return candidateIdentity{itemID: item.ID, channelID: item.ChannelID, modelName: item.ModelName}
}

func (it *Iterator) selectionMetricsFromSnapshot(item model.GroupItem, snapshot channelHealthSnapshot) model.ChannelSelectionMetrics {
	var retryAt *time.Time
	if !snapshot.CooldownUntil.IsZero() {
		deadline := snapshot.CooldownUntil
		retryAt = &deadline
	}
	snapshot.DynamicScore = healthPenalty(snapshot, globalChannelHealth.now())
	var baseRank *int
	compositeScore := snapshot.DynamicScore
	if rank, ok := it.healthBaseRanks[groupItemIdentity(item)]; ok {
		rankCopy := rank
		baseRank = &rankCopy
		compositeScore += float64(rank) * channelBaseRankPenalty
	}
	return model.ChannelSelectionMetrics{
		BaseRank:            baseRank,
		Priority:            item.Priority,
		DynamicScore:        snapshot.DynamicScore,
		CompositeScore:      compositeScore,
		InFlight:            snapshot.InFlight,
		RecentLoad:          snapshot.RecentLoad,
		FailureRate:         snapshot.FailureRate,
		LatencyMillis:       snapshot.LatencyMillis,
		ConsecutiveFailures: snapshot.ConsecutiveFailure,
		CooldownUntil:       retryAt,
	}
}

func (it *Iterator) releaseCurrentReservation() {
	if it == nil || it.currentReservation == nil {
		return
	}
	reservation := it.currentReservation
	it.currentReservation = nil
	reservation.release()
}

func (it *Iterator) releaseReservation(reservation *channelHealthReservation) {
	if reservation == nil {
		return
	}
	if it != nil && it.currentReservation == reservation {
		it.currentReservation = nil
	}
	reservation.release()
}

// Close releases an attempt reservation if a request exits before its span can
// complete normally.
func (it *Iterator) Close() {
	it.releaseCurrentReservation()
}

// Item 返回当前候选的 GroupItem
func (it *Iterator) Item() model.GroupItem {
	return it.candidates[it.index]
}

// IsSticky 当前候选是否为粘性通道
func (it *Iterator) IsSticky() bool {
	_, ok := it.preferences[it.index]
	return ok
}

func (it *Iterator) StickyKeyID() int {
	preference, ok := it.preferences[it.index]
	if !ok {
		return 0
	}
	return preference.entry.ChannelKeyID
}

func (it *Iterator) PreferenceSource() PreferenceSource {
	preference, ok := it.preferences[it.index]
	if !ok {
		return PreferenceNone
	}
	return preference.source
}

func (it *Iterator) selectionTier() (candidateTier, bool) {
	if it == nil || it.index < 0 || it.index >= len(it.candidates) {
		return candidateTier{}, false
	}
	for _, tier := range it.tiers {
		if it.index >= tier.start && it.index < tier.end {
			return tier, true
		}
	}
	return candidateTier{}, false
}

// SelectionReason and SelectionStrategy expose stable routing metadata for
// attempt logs without coupling the relay package to balancer internals.
func (it *Iterator) SelectionReason() string {
	return it.PreferenceSource().String()
}

func (it *Iterator) SelectionStrategy() string {
	if tier, ok := it.selectionTier(); ok {
		return groupModeName(tier.mode)
	}
	return groupModeName(it.mode)
}

func groupModeName(mode model.GroupMode) string {
	switch mode {
	case model.GroupModeRoundRobin:
		return "round_robin"
	case model.GroupModeFailover:
		return "failover"
	case model.GroupModeWeighted:
		return "weighted"
	default:
		return "round_robin"
	}
}

func (it *Iterator) QualityRank() int {
	if preference, ok := it.preferences[it.index]; ok {
		return preference.qualityRank
	}
	if tier, ok := it.selectionTier(); ok {
		return tier.scope.qualityTier
	}
	return 0
}

// InvalidateCurrentPreference clears ordinary affinity after the preferred
// channel proves unusable before downstream payload is written. Replay state
// and replay routing preferences remain independent and untouched.
func (it *Iterator) InvalidateCurrentPreference() {
	if it.strictAffinity || it.readOnly {
		return
	}
	preference, ok := it.preferences[it.index]
	if !ok || preference.source == PreferenceResponsesReplay {
		return
	}
	DeleteRoutingAffinity(it.apiKeyID, it.groupID, it.requestModel, it.affinity)
	for index, remainingPreference := range it.preferences {
		if index >= it.index && remainingPreference.source != PreferenceResponsesReplay {
			delete(it.preferences, index)
		}
	}
}

func (it *Iterator) RecordAffinity(channelID, keyID int) {
	if it.readOnly {
		return
	}
	SetRoutingAffinity(it.apiKeyID, it.groupID, it.requestModel, channelID, keyID, it.affinity)
}

func (it *Iterator) Affinity() AffinityOptions { return it.affinity }

func (it *Iterator) StrictAffinity() bool { return it.strictAffinity }

func (it *Iterator) SelectionMetrics() *model.ChannelSelectionMetrics {
	return cloneSelectionMetrics(it.selectionMetrics)
}

// Len 返回候选列表长度
func (it *Iterator) Len() int {
	return len(it.candidates)
}

// HasRemainingDifferentChannel reports whether a later candidate belongs to a
// different channel. It is used by retry policy decisions that should bypass a
// rate-limited channel only when an actual channel-level fallback remains.
func (it *Iterator) HasRemainingDifferentChannel(channelID int) bool {
	return it.HasRemainingDifferentChannelExcept(channelID, nil)
}

// HasRemainingDifferentChannelExcept is the exclusion-aware variant used when
// the current request has already ruled out one or more channels.
func (it *Iterator) HasRemainingDifferentChannelExcept(channelID int, excluded map[int]struct{}) bool {
	return it.HasRemainingDifferentChannelMatching(channelID, excluded, nil)
}

// HasRemainingDifferentChannelMatching also requires accept to approve the
// candidate channel. A nil predicate accepts every non-excluded channel.
func (it *Iterator) HasRemainingDifferentChannelMatching(channelID int, excluded map[int]struct{}, accept func(int) bool) bool {
	return it.HasRemainingDifferentCandidateMatching(channelID, excluded, func(item model.GroupItem) bool {
		return accept == nil || accept(item.ChannelID)
	})
}

// HasRemainingDifferentCandidateMatching lets the relay check model capability
// and key availability without advancing the iterator or acquiring a probe.
func (it *Iterator) HasRemainingDifferentCandidateMatching(channelID int, excluded map[int]struct{}, accept func(model.GroupItem) bool) bool {
	if it == nil {
		return false
	}
	for index := it.index + 1; index < len(it.candidates); index++ {
		candidateChannelID := it.candidates[index].ChannelID
		if candidateChannelID == channelID {
			continue
		}
		if _, skip := excluded[candidateChannelID]; skip {
			continue
		}
		if accept != nil && !accept(it.candidates[index]) {
			continue
		}
		return true
	}
	return false
}

// Index 返回当前迭代位置（0-based）
func (it *Iterator) Index() int {
	return it.index
}

// Skip 记录当前通道被跳过（通道禁用、无Key、类型不兼容等）
func (it *Iterator) Skip(channelID, channelKeyID int, channelName, msg string) {
	it.skip(channelID, channelKeyID, channelName, msg, CapabilityTrace{})
}

// SkipWithCapability records a planner rejection before an upstream attempt.
func (it *Iterator) SkipWithCapability(channelID, channelKeyID int, channelName, msg string, trace CapabilityTrace) {
	it.skip(channelID, channelKeyID, channelName, msg, trace)
}

func (it *Iterator) skip(channelID, channelKeyID int, channelName, msg string, trace CapabilityTrace) {
	sticky := it.IsSticky()
	it.count++
	it.attempts = append(it.attempts, model.ChannelAttempt{
		ChannelID:         channelID,
		ChannelKeyID:      channelKeyID,
		ChannelName:       channelName,
		ModelName:         it.candidates[it.index].ModelName,
		AttemptNum:        it.count,
		Status:            model.AttemptSkipped,
		Sticky:            sticky,
		Msg:               msg,
		AdapterType:       trace.AdapterType,
		CapabilityStatus:  trace.Status,
		CapabilityPolicy:  trace.Policy,
		ConversionPath:    append([]string(nil), trace.ConversionPath...),
		RequiredFeatures:  append([]string(nil), trace.RequiredFeatures...),
		DegradedFields:    append([]string(nil), trace.DegradedFields...),
		CapabilityLosses:  append([]model.CapabilityLoss(nil), trace.Losses...),
		Conversion:        cloneRequestConversion(trace.Conversion),
		Lossiness:         trace.Lossiness,
		CapabilityReasons: append([]string(nil), trace.Reasons...),
		FallbackReason:    msg,
		SelectionReason:   it.SelectionReason(),
		SelectionStrategy: it.SelectionStrategy(),
		QualityRank:       it.QualityRank(),
		CandidateCount:    len(it.candidates),
		SelectionMetrics:  cloneSelectionMetrics(it.selectionMetrics),
	})
	it.releaseCurrentReservation()
	it.InvalidateCurrentPreference()
}

// SkipCircuitBreak 检查熔断状态，若已熔断自动记录（含剩余冷却时间）并返回 true
func (it *Iterator) SkipCircuitBreak(channelID, channelKeyID int, channelName string) bool {
	modelName := it.candidates[it.index].ModelName
	tripped, remaining := IsTripped(channelID, channelKeyID, modelName)
	if !tripped {
		return false
	}
	msg := "circuit breaker tripped"
	if remaining > 0 {
		seconds := int((remaining + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		msg = fmt.Sprintf("circuit breaker tripped, remaining cooldown: %ds", seconds)
	}
	it.count++
	it.attempts = append(it.attempts, model.ChannelAttempt{
		ChannelID:         channelID,
		ChannelKeyID:      channelKeyID,
		ChannelName:       channelName,
		ModelName:         modelName,
		AttemptNum:        it.count,
		Status:            model.AttemptCircuitBreak,
		Sticky:            it.IsSticky(),
		Msg:               msg,
		FallbackReason:    msg,
		FailureClass:      "circuit_open",
		SelectionReason:   it.SelectionReason(),
		SelectionStrategy: it.SelectionStrategy(),
		QualityRank:       it.QualityRank(),
		CandidateCount:    len(it.candidates),
		SelectionMetrics:  cloneSelectionMetrics(it.selectionMetrics),
	})
	it.releaseCurrentReservation()
	if retryAt, ok := RetryAt(channelID, channelKeyID, modelName); ok {
		attempt := &it.attempts[len(it.attempts)-1]
		attempt.RetryAt = &retryAt
	}
	return true
}

// StartAttempt 开始一次真实转发尝试，返回 Span 用于记录结果
func (it *Iterator) StartAttempt(channelID, channelKeyID int, channelName string) *AttemptSpan {
	it.count++
	if it.currentReservation == nil || !it.currentReservation.active.Load() {
		reservation, snapshot := globalChannelHealth.reserve(channelID, it.candidates[it.index].ModelName)
		it.currentReservation = reservation
		it.selectionMetrics = it.selectionMetricsFromSnapshot(it.candidates[it.index], snapshot)
	}
	return &AttemptSpan{
		attempt: model.ChannelAttempt{
			ChannelID:         channelID,
			ChannelKeyID:      channelKeyID,
			ChannelName:       channelName,
			ModelName:         it.candidates[it.index].ModelName,
			AttemptNum:        it.count,
			Sticky:            it.IsSticky(),
			SelectionReason:   it.SelectionReason(),
			SelectionStrategy: it.SelectionStrategy(),
			QualityRank:       it.QualityRank(),
			CandidateCount:    len(it.candidates),
			SelectionMetrics:  cloneSelectionMetrics(it.selectionMetrics),
		},
		startTime:   time.Now(),
		iter:        it,
		reservation: it.currentReservation,
	}
}

func cloneSelectionMetrics(metrics model.ChannelSelectionMetrics) *model.ChannelSelectionMetrics {
	cloned := metrics
	if metrics.CooldownUntil != nil {
		deadline := *metrics.CooldownUntil
		cloned.CooldownUntil = &deadline
	}
	if metrics.BaseRank != nil {
		rank := *metrics.BaseRank
		cloned.BaseRank = &rank
	}
	return &cloned
}

// Attempts 返回所有决策记录（交给日志模块持久化）
func (it *Iterator) Attempts() []model.ChannelAttempt {
	return it.attempts
}

// AttemptSpan 管理单次通道尝试的生命周期（计时、状态、结果）
type AttemptSpan struct {
	attempt     model.ChannelAttempt
	startTime   time.Time
	iter        *Iterator
	ended       bool
	reservation *channelHealthReservation
}

// End 结束尝试：设置状态，自动计算耗时，追加到 Iterator
func (s *AttemptSpan) End(status model.AttemptStatus, statusCode int, msg string) {
	if s.ended {
		return
	}
	s.ended = true
	duration := time.Since(s.startTime)
	s.attempt.Status = status
	s.attempt.Duration = int(duration.Milliseconds())
	s.attempt.Msg = msg
	if s.reservation != nil {
		globalChannelHealth.record(s.attempt.ChannelID, s.attempt.ModelName, status, duration, s.attempt.FailureClass, valueOrZeroTime(s.attempt.RetryAt), s.attempt.FailureScope)
	}
	if s.iter != nil {
		s.iter.releaseReservation(s.reservation)
	} else if s.reservation != nil {
		s.reservation.release()
	}
	if status == model.AttemptFailed && msg != "" {
		s.attempt.FallbackReason = msg
	}
	if s.iter != nil {
		s.iter.attempts = append(s.iter.attempts, s.attempt)
	}
}

func valueOrZeroTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// SetAdapterType records the outbound protocol selected for this attempt.
func (s *AttemptSpan) SetAdapterType(adapterType string) {
	s.attempt.AdapterType = adapterType
}

func (s *AttemptSpan) SetHTTPDetails(id string, status *int, requestID string) {
	s.attempt.AttemptID = id
	s.attempt.HTTPStatus = status
	s.attempt.UpstreamRequestID = requestID
}

func (s *AttemptSpan) SetTransport(transport string, mode model.RelayLogWSMode, recovery model.RelayLogWSRecovery) {
	s.attempt.Transport = transport
	s.attempt.WSMode = mode
	s.attempt.Recovery = recovery
}

func (s *AttemptSpan) SetStreamDiagnostics(diagnostics *model.StreamDiagnostics) {
	if diagnostics == nil {
		return
	}
	snapshot := *diagnostics
	s.attempt.Stream = &snapshot
}

// SetCapability records the semantic planning decision that authorized this attempt.
func (s *AttemptSpan) SetCapability(trace CapabilityTrace) {
	if trace.AdapterType != "" {
		s.attempt.AdapterType = trace.AdapterType
	}
	s.attempt.CapabilityStatus = trace.Status
	s.attempt.CapabilityPolicy = trace.Policy
	s.attempt.ConversionPath = append([]string(nil), trace.ConversionPath...)
	s.attempt.RequiredFeatures = append([]string(nil), trace.RequiredFeatures...)
	s.attempt.DegradedFields = append([]string(nil), trace.DegradedFields...)
	s.attempt.CapabilityLosses = append([]model.CapabilityLoss(nil), trace.Losses...)
	s.attempt.Conversion = cloneRequestConversion(trace.Conversion)
	s.attempt.Lossiness = trace.Lossiness
	s.attempt.CapabilityReasons = append([]string(nil), trace.Reasons...)
}

// SetFailure records the structured relay failure without importing relay
// (which would create a package cycle). RetryAt is copied so later mutation of
// the caller's time value cannot alter the persisted attempt.
func (s *AttemptSpan) SetFailure(class string, retryable bool, retryAt time.Time, scope ...string) {
	s.attempt.FailureClass = class
	if len(scope) > 0 {
		s.attempt.FailureScope = scope[0]
	}
	s.attempt.Retryable = retryable
	if !retryAt.IsZero() {
		deadline := retryAt
		s.attempt.RetryAt = &deadline
	}
}

// Duration 返回从开始到现在的耗时
func (s *AttemptSpan) Duration() time.Duration {
	return time.Since(s.startTime)
}
