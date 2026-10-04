package web

import (
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// BranchCard backs one list entry on branches.gohtml.
type BranchCard struct {
	Index      int
	Number     int
	Name       string
	City       string
	Address    string
	Hours      string
	RouteURL   string // 2GIS link (exact point when coordinates are set)
	EmbedURL   string // Google Maps iframe source
	FirstInSet bool
}

// BranchesData backs branches.gohtml.
type BranchesData struct {
	Cards []BranchCard
	Empty bool
}

// buildBranchesData turns the repo's flat branch list into the pseudo-map
// pins + list view model. The first branch is selected by default,
// matching the design's initial state; the actual click-to-select
// interaction is handled client-side (see web/static/js/branches.js) since
// it needs no server round trip.
func buildBranchesData(branches []storefront.Branch) BranchesData {
	data := BranchesData{Empty: len(branches) == 0}
	for i, b := range branches {
		data.Cards = append(data.Cards, BranchCard{
			Index:      i,
			Number:     i + 1,
			Name:       b.Name,
			City:       b.City,
			Address:    b.Address,
			Hours:      b.WorkingHours,
			RouteURL:   b.MapURL(),
			EmbedURL:   b.EmbedURL(),
			FirstInSet: i == 0,
		})
	}
	return data
}

func (h *handlers) branches(w http.ResponseWriter, r *http.Request) error {
	data := h.base(r, "branches")

	branches, err := h.branchRepo.List(r.Context())
	if err != nil {
		return err
	}
	data.Data = buildBranchesData(branches)

	return h.render.Render(w, "branches", data)
}
