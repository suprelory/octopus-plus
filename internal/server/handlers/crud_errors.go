package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/suprelory/octopus-plus/internal/apperror"
)

func channelError(code string, message string, err error) *apperror.Error {
	return apperror.Wrap(code, message, err).WithStatus(mutationErrorStatus(err))
}

// mutationErrorStatus maps validation-like operation errors to a client error
// while keeping unexpected failures at 500. Operation packages intentionally
// return plain errors, so this is the handler boundary where their status is
// translated into HTTP semantics.
func mutationErrorStatus(err error) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	var appErr *apperror.Error
	if errors.As(err, &appErr) && appErr != nil && appErr.Status > 0 {
		return appErr.Status
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "not found"):
		return http.StatusNotFound
	case strings.Contains(message, "required"),
		strings.Contains(message, "invalid"),
		strings.Contains(message, "duplicate"),
		strings.Contains(message, "already exists"),
		strings.Contains(message, "json object"),
		strings.Contains(message, "unsupported"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func groupError(code string, message string, err error) *apperror.Error {
	return apperror.Wrap(code, message, err).WithStatus(mutationErrorStatus(err))
}

func modelError(code string, message string, err error) *apperror.Error {
	return apperror.Wrap(code, message, err).WithStatus(http.StatusInternalServerError)
}

const (
	codeChannelNotFound          = "channel.not_found"
	codeChannelCreateFailed      = "channel.create_failed"
	codeChannelUpdateFailed      = "channel.update_failed"
	codeChannelDeleteFailed      = "channel.delete_failed"
	codeChannelFetchModelsFailed = "channel.fetch_models_failed"

	codeGroupNotFound     = "group.not_found"
	codeGroupCreateFailed = "group.create_failed"
	codeGroupUpdateFailed = "group.update_failed"
	codeGroupDeleteFailed = "group.delete_failed"
	codeGroupPinFailed    = "group.pin_failed"

	codeGroupPresetListFailed        = "group.preset.list_failed"
	codeGroupPresetCreateFailed      = "group.preset.create_failed"
	codeGroupPresetCreateBlankFailed = "group.preset.create_blank_failed"
	codeGroupPresetCloneFailed       = "group.preset.clone_failed"
	codeGroupPresetUpdateFailed      = "group.preset.update_failed"
	codeGroupPresetDeleteFailed      = "group.preset.delete_failed"
	codeGroupPresetActivateFailed    = "group.preset.activate_failed"

	codeModelPriceUpdateFailed = "model.price_update_failed"
	codeModelPriceDeleteFailed = "model.price_delete_failed"
	codeModelCreateFailed      = "model.create_failed"
	codeModelUpdateFailed      = "model.update_failed"
	codeModelDeleteFailed      = "model.delete_failed"
)
