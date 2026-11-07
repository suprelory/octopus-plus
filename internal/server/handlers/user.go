package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/auth"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/user").
		Use(noStoreUserResponse).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/login", http.MethodPost).
				Handle(login),
		).
		AddRoute(
			router.NewRoute("/bootstrap", http.MethodGet).
				Handle(bootstrapStatus),
		).
		AddRoute(
			router.NewRoute("/bootstrap", http.MethodPost).
				Handle(bootstrap),
		)
	router.NewGroupRouter("/api/v1/user").
		Use(noStoreUserResponse).
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/refresh", http.MethodPost).
				Handle(refresh),
		).
		AddRoute(
			router.NewRoute("/change-password", http.MethodPost).
				Handle(changePassword),
		).
		AddRoute(
			router.NewRoute("/change-username", http.MethodPost).
				Handle(changeUsername),
		).
		AddRoute(
			router.NewRoute("/status", http.MethodGet).
				Handle(status),
		)
}

func noStoreUserResponse(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func login(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var user model.UserLogin
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.InvalidJSON(c)
		return
	}
	// expire 只接受 0（默认）、-1（记住我）和正数分钟数。历史实现把任意值直接
	// 传给 GenerateJWTToken，expire < -1 会签出没有 exp 声明的 token 并 panic。
	if !auth.JWTExpireValid(user.Expire) {
		resp.InvalidParam(c)
		return
	}

	source := middleware.ClientIP(c)
	if allowed, retryAfter := op.LoginAttemptAllow(source); !allowed {
		middleware.RecordAuthEvent(c, "login.rejected", apperror.CodeAuthLoginRateLimited, 0)
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		resp.ErrorWithAppError(c, http.StatusTooManyRequests,
			apperror.New(apperror.CodeAuthLoginRateLimited, "too many login attempts").
				WithStatus(http.StatusTooManyRequests))
		return
	}

	verifiedUser, err := op.UserVerifyAndGet(user.Username, user.Password)
	if err != nil {
		if apperror.IsCode(err, apperror.CodeAuthBootstrapRequired) {
			middleware.RecordAuthEvent(c, "login.rejected", apperror.CodeAuthBootstrapRequired, 0)
			resp.ErrorWithAppError(c, http.StatusServiceUnavailable, err)
			return
		}
		op.LoginAttemptFailed(source)
		middleware.RecordAuthEvent(c, "login.rejected", apperror.CodeAuthInvalidCredentials, 0)
		resp.InvalidCredentials(c)
		return
	}
	op.LoginAttemptSucceeded(source)

	token, expire, err := auth.GenerateJWTTokenForUser(user.Expire, verifiedUser)
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	middleware.RecordAuthEvent(c, "login.succeeded", "", 0)
	resp.Success(c, model.UserLoginResponse{Token: token, ExpireAt: expire})
}

func refresh(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	user, ok := middleware.AuthenticatedAdminUser(c)
	claims, claimsOK := middleware.AuthenticatedAdminClaims(c)
	if !ok || !claimsOK {
		resp.InvalidToken(c)
		return
	}
	token, expire, err := auth.RenewJWTTokenForUser(claims, user)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrExpiredToken):
			resp.ExpiredToken(c)
		case errors.Is(err, auth.ErrInvalidToken):
			resp.InvalidToken(c)
		default:
			resp.InternalErrorWithLog(c, err)
		}
		return
	}
	if token == "" {
		c.Status(http.StatusNoContent)
		return
	}
	resp.Success(c, model.UserLoginResponse{Token: token, ExpireAt: expire})
}

func bootstrapStatus(c *gin.Context) {
	required, err := op.UserBootstrapRequired()
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, model.UserBootstrapStatus{Required: required})
}

func bootstrap(c *gin.Context) {
	var input model.UserBootstrap
	if err := c.ShouldBindJSON(&input); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.UserBootstrap(input.Username, input.Password, input.Token); err != nil {
		middleware.RecordAuthEvent(c, "bootstrap.rejected", apperror.Code(err), 0)
		resp.ErrorWithAppError(c, http.StatusInternalServerError, err)
		return
	}
	resp.Success(c, "administrator initialized successfully")
}

func changePassword(c *gin.Context) {
	var user model.UserChangePassword
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.UserChangePassword(user.OldPassword, user.NewPassword); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, err)
		return
	}
	resp.Success(c, "password changed successfully")
}

func changeUsername(c *gin.Context) {
	var user model.UserChangeUsername
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.UserChangeUsername(user.NewUsername); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, "username changed successfully")
}

func status(c *gin.Context) {
	resp.Success(c, "ok")
}
