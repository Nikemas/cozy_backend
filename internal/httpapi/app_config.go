package httpapi

import (
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/config"
)

// appConfigResponse is what the Flutter app reads at startup to decide
// whether to force (below min_version) or suggest (below latest_version)
// an update, and where to send the user to do it.
type appConfigResponse struct {
	MinVersion      string `json:"min_version"`
	LatestVersion   string `json:"latest_version"`
	StoreURLIOS     string `json:"store_url_ios"`
	StoreURLAndroid string `json:"store_url_android"`
}

// RegisterAppConfigRoutes mounts the public GET /api/v1/app/config.
func RegisterAppConfigRoutes(mux *http.ServeMux, cfg *config.Config) {
	mux.Handle("GET /api/v1/app/config", apperr.Wrap(appConfigHandler(cfg)))
}

func appConfigHandler(cfg *config.Config) apperr.HandlerFunc {
	body := appConfigResponse{
		MinVersion:      cfg.AppMinVersion,
		LatestVersion:   cfg.AppLatestVersion,
		StoreURLIOS:     cfg.AppStoreURLIOS,
		StoreURLAndroid: cfg.AppStoreURLAndroid,
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		// Short cache: a bumped APP_MIN_VERSION should reach clients within
		// minutes of a redeploy.
		w.Header().Set("Cache-Control", "public, max-age=300")
		return writeJSON(w, http.StatusOK, body)
	}
}
