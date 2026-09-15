package points

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

// RegisterRoutes mounts the admin points-of-sale endpoints under
// /admin/api/points, gated to staff.RoleOwner only (per Task L — unlike
// admin catalog routes, managers and point_staff have no access here).
func RegisterRoutes(mux *http.ServeMux, db *sql.DB, staffSvc *staff.Service) {
	repo := NewPointsRepo(db)
	ownerOnly := staffSvc.RequireRole(staff.RoleOwner)

	mux.Handle("GET /admin/api/points", ownerOnly(apperr.Wrap(listPointsHandler(repo))))
	mux.Handle("POST /admin/api/points", ownerOnly(apperr.Wrap(createPointHandler(repo))))
	mux.Handle("PUT /admin/api/points/{id}", ownerOnly(apperr.Wrap(updatePointHandler(repo))))
	mux.Handle("DELETE /admin/api/points/{id}", ownerOnly(apperr.Wrap(deletePointHandler(repo))))
}

// createPointRequest is the POST body: {name, address}. is_active isn't
// accepted here — a newly created point always defaults to active.
type createPointRequest struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

func (req createPointRequest) toInput() PointInput {
	return PointInput{
		Name:     req.Name,
		Address:  req.Address,
		IsActive: true,
	}
}

// updatePointRequest is the PUT body: {name, address, is_active} — unlike
// create, is_active is an explicit, required field so a point can be
// deactivated/reactivated through this endpoint.
type updatePointRequest struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	IsActive bool   `json:"is_active"`
}

func (req updatePointRequest) toInput() PointInput {
	return PointInput(req)
}

func listPointsHandler(repo *PointsRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		list, err := repo.List(r.Context())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, list)
	}
}

func createPointHandler(repo *PointsRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req createPointRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		p, err := repo.Create(r.Context(), req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, p)
	}
}

func updatePointHandler(repo *PointsRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req updatePointRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		p, err := repo.Update(r.Context(), r.PathValue("id"), req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, p)
	}
}

func deletePointHandler(repo *PointsRepo) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if err := repo.Delete(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// decodeJSON/writeJSON mirror internal/httpapi's unexported helpers of the
// same name (internal/httpapi/auth.go) — those are private to package
// httpapi, so this package carries its own copies rather than importing
// them.

func decodeJSON(r *http.Request, dst any) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return apperr.BadRequest("bad_request", "некорректное тело запроса")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(body)
}
