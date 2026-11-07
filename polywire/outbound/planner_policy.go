package outbound

import "github.com/suprelory/octopus-plus/polywire/model"

type LossAction = model.RequestTransformationAction

const (
	LossActionPreserve  = model.RequestTransformationPreserve
	LossActionTranslate = model.RequestTransformationTranslate
	LossActionDrop      = model.RequestTransformationDrop
	LossActionTruncate  = model.RequestTransformationTruncate
	LossActionRepair    = model.RequestTransformationRepair
	LossActionReject    = model.RequestTransformationReject
)

// Planning and request construction share the adapter's exact report shape.
type CapabilityLoss = model.RequestTransformationChange
type LossReport []model.RequestTransformationChange
