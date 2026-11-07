package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/relay"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/router"
)

func init() {
	router.NewGroupRouter("/v1/images").
		Use(middleware.APIKeyAuth()).
		AddRoute(
			router.NewRoute("/generations", http.MethodPost).
				Handle(generations),
		).
		AddRoute(
			router.NewRoute("/edits", http.MethodPost).
				Handle(edits),
		).
		AddRoute(
			router.NewRoute("/variations", http.MethodPost).
				Handle(variations),
		)
}

func generations(c *gin.Context) {
	relay.ImagesHandler("/images/generations", c)
}

func edits(c *gin.Context) {
	relay.ImagesHandler("/images/edits", c)
}

func variations(c *gin.Context) {
	relay.ImagesHandler("/images/variations", c)
}
