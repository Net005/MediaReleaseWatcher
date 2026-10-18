package app

import "net/http"

func (a *App) registerUpgradeRoutes(mux *http.ServeMux) {
	a.registerScheduleRoutes(mux)
	a.registerSiloRoutes(mux)
	a.registerArrSyncRoutes(mux)
	a.registerWatchlistRoutes(mux)
}
