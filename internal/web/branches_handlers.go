package web

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// pinPalette holds hand-picked % coordinates for the decorative pseudo-map
// (COZY_WEB_DESIGN.md §3.7: "CSS-сетка + фиксированные % координаты пинов,
// без реальной геокарты/API"). points_of_sale has no lat/lng columns, so
// pins are positioned by this fixed, hand-authored layout instead of real
// geocoding — spread across the map so pins don't overlap for a handful of
// branches. If there are ever more branches than palette entries, it
// cycles rather than panicking.
var pinPalette = [][2]float64{
	{38, 30},
	{64, 52},
	{24, 78},
	{80, 24},
	{52, 68},
	{14, 48},
	{72, 82},
	{90, 58},
}

// pinCoords returns the (x%, y%) position of the index-th branch's pin on
// the pseudo-map.
func pinCoords(index int) (x, y float64) {
	p := pinPalette[index%len(pinPalette)]
	return p[0], p[1]
}

// distanceStubs are placeholder "distance to branch" labels
// (COZY_WEB_DESIGN.md §3.7 explicitly calls this a stub: real distance
// needs the customer's own geolocation, which is an open question in
// tasks/web-plan.md, not something Task 5 resolves).
//
// TODO(geo): replace with a real distance once customer geolocation (or a
// delivery-address-based estimate) is available.
var distanceStubs = []string{"0.8 км", "2.1 км", "4.5 км", "6.2 км", "8.0 км"}

func distanceStub(index int) string {
	return distanceStubs[index%len(distanceStubs)]
}

// BranchPin backs one pin on branches.gohtml's pseudo-map. Index is the
// 0-based position (used to match a pin to its list card client-side);
// Number is the 1-based label shown inside the pin/badge.
type BranchPin struct {
	Index      int
	Number     int
	X, Y       float64
	FirstInSet bool
}

// BranchCard backs one list entry on branches.gohtml.
type BranchCard struct {
	Index      int
	Number     int
	Name       string
	Address    string
	Distance   string
	RouteURL   string // external map search link — no embedded map/API
	FirstInSet bool
}

// BranchesData backs branches.gohtml.
type BranchesData struct {
	Pins  []BranchPin
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
		x, y := pinCoords(i)
		data.Pins = append(data.Pins, BranchPin{Index: i, Number: i + 1, X: x, Y: y, FirstInSet: i == 0})
		data.Cards = append(data.Cards, BranchCard{
			Index:      i,
			Number:     i + 1,
			Name:       b.Name,
			Address:    b.Address,
			Distance:   distanceStub(i),
			RouteURL:   routeURL(b.Address),
			FirstInSet: i == 0,
		})
	}
	return data
}

// routeURL builds a Google Maps search link for address — a plain external
// link, not an embedded map or a geocoding API call from our own server,
// so it doesn't need an API key and stays within "no real map integration"
// per web-plan Task 5.
func routeURL(address string) string {
	return "https://www.google.com/maps/search/?api=1&query=" + url.QueryEscape(fmt.Sprintf("Cozy, %s, Бишкек", address))
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
