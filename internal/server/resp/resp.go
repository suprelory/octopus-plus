package resp

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/apperror"
)

type ResponseStruct struct {
	Code      int            `json:"code" example:"200"`
	ErrorCode string         `json:"error_code,omitempty" example:"site.sub2api.api_key_required"`
	Message   string         `json:"message" example:"success"`
	Params    map[string]any `json:"params,omitempty"`
	Data      interface{}    `json:"data,omitempty"`
}

func Success(c *gin.Context, data any) {
	recordResourceID(c, data)
	c.JSON(http.StatusOK, ResponseStruct{
		Code:    http.StatusOK,
		Message: "success",
		Data:    data,
	})
}

func Error(c *gin.Context, code int, err string) {
	ErrorWithCode(c, code, "", err)
}

func ErrorWithAppError(c *gin.Context, fallbackStatus int, err error) {
	status := fallbackStatus
	if appStatus := apperror.Status(err); appStatus != 0 {
		status = appStatus
	}
	recordError(c, apperror.Code(err), apperror.Message(err), err)
	ErrorWithCodeAndParams(c, status, apperror.Code(err), apperror.Message(err), apperror.Params(err))
}

func ErrorWithCode(c *gin.Context, status int, errorCode string, message string) {
	ErrorWithCodeAndParams(c, status, errorCode, message, nil)
}

func ErrorWithCodeAndParams(c *gin.Context, status int, errorCode string, message string, params map[string]any) {
	recordError(c, errorCode, message, nil)
	c.AbortWithStatusJSON(status, ResponseStruct{
		Code:      status,
		ErrorCode: errorCode,
		Message:   message,
		Params:    params,
	})
}

func InvalidJSON(c *gin.Context) {
	ErrorWithAppError(c, http.StatusBadRequest, apperror.InvalidJSON(ErrInvalidJSON))
}

func InvalidParam(c *gin.Context) {
	ErrorWithAppError(c, http.StatusBadRequest, apperror.InvalidParam(ErrInvalidParam))
}

func InternalError(c *gin.Context) {
	ErrorWithAppError(c, http.StatusInternalServerError, apperror.New(apperror.CodeCommonInternalError, ErrInternalServer).WithStatus(http.StatusInternalServerError))
}

// InternalErrorWithLog 给客户端返回通用文案，把真实错误交给请求日志统一记录。
//
// 直接 resp.Error(c, 500, err.Error()) 会把 SQL 文本、文件路径、上游 URL 泄给
// 客户端，同时绕过 apperror 的错误码体系。带 apperror 码的错误按原样返回
// （那是给前端翻译用的稳定标识，本来就不含内部细节）。
func InternalErrorWithLog(c *gin.Context, err error) {
	if err == nil {
		InternalError(c)
		return
	}
	if code := apperror.Code(err); code != "" {
		ErrorWithAppError(c, http.StatusInternalServerError, err)
		return
	}

	ErrorWithAppError(c, http.StatusInternalServerError,
		apperror.Wrap(apperror.CodeCommonInternalError, ErrInternalServer, err).WithStatus(http.StatusInternalServerError))
}

func NotFound(c *gin.Context) {
	ErrorWithAppError(c, http.StatusNotFound, apperror.New(apperror.CodeCommonNotFound, ErrResourceNotFound).WithStatus(http.StatusNotFound))
}

func Unauthorized(c *gin.Context) {
	ErrorWithAppError(c, http.StatusUnauthorized, apperror.New(apperror.CodeAuthUnauthorized, ErrUnauthorized).WithStatus(http.StatusUnauthorized))
}

func InvalidToken(c *gin.Context) {
	ErrorWithAppError(c, http.StatusUnauthorized, apperror.New(apperror.CodeAuthInvalidToken, ErrUnauthorized).WithStatus(http.StatusUnauthorized))
}

func ExpiredToken(c *gin.Context) {
	ErrorWithAppError(c, http.StatusUnauthorized, apperror.New(apperror.CodeAuthExpiredToken, "administrator token has expired").WithStatus(http.StatusUnauthorized))
}

func InvalidCredentials(c *gin.Context) {
	ErrorWithAppError(c, http.StatusUnauthorized, apperror.New(apperror.CodeAuthInvalidCredentials, ErrUnauthorized).WithStatus(http.StatusUnauthorized))
}

func APIKeyMissing(c *gin.Context) {
	ErrorWithAppError(c, http.StatusUnauthorized, apperror.New(apperror.CodeAuthAPIKeyMissing, "API key is missing").WithStatus(http.StatusUnauthorized))
}

func APIKeyExpired(c *gin.Context) {
	ErrorWithAppError(c, http.StatusUnauthorized, apperror.New(apperror.CodeAuthAPIKeyExpired, "API key has expired").WithStatus(http.StatusUnauthorized))
}
