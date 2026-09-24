package web

import (
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/storefront"
)

func TestPinCoordsWithinMapBoundsAndCycles(t *testing.T) {
	for i := 0; i < len(pinPalette)*3; i++ {
		x, y := pinCoords(i)
		if x < 0 || x > 100 || y < 0 || y > 100 {
			t.Fatalf("pinCoords(%d) = (%v, %v), want both in [0,100]", i, x, y)
		}
	}

	// Cycles: index i and i+len(palette) must land on the same pin.
	x1, y1 := pinCoords(2)
	x2, y2 := pinCoords(2 + len(pinPalette))
	if x1 != x2 || y1 != y2 {
		t.Errorf("pinCoords should cycle through the palette: (%v,%v) != (%v,%v)", x1, y1, x2, y2)
	}
}

func TestRouteURLEscapesAddress(t *testing.T) {
	url := routeURL("ул. Ибраимова, 115")
	if !strings.HasPrefix(url, "https://www.google.com/maps/search/?api=1&query=") {
		t.Fatalf("routeURL = %q, want a Google Maps search URL", url)
	}
	if strings.Contains(url, " ") {
		t.Errorf("routeURL = %q, address should be query-escaped (no raw spaces)", url)
	}
}

func TestBuildBranchesDataEmpty(t *testing.T) {
	data := buildBranchesData(nil)
	if !data.Empty {
		t.Error("buildBranchesData(nil).Empty = false, want true")
	}
	if len(data.Pins) != 0 || len(data.Cards) != 0 {
		t.Error("buildBranchesData(nil) should produce no pins/cards")
	}
}

func TestBuildBranchesDataAssignsSequentialPinsAndMarksFirstCard(t *testing.T) {
	branches := []storefront.Branch{
		{ID: "1", Name: "Cozy Bishkek Park", Address: "ул. Ибраимова, 115"},
		{ID: "2", Name: "Cozy Vefa Center", Address: "просп. Чуй, 219"},
	}
	data := buildBranchesData(branches)

	if data.Empty {
		t.Fatal("buildBranchesData(2 branches).Empty = true, want false")
	}
	if len(data.Pins) != 2 || len(data.Cards) != 2 {
		t.Fatalf("got %d pins / %d cards, want 2/2", len(data.Pins), len(data.Cards))
	}
	if !data.Cards[0].FirstInSet || data.Cards[1].FirstInSet {
		t.Error("only the first card should have FirstInSet = true")
	}
	for i, p := range data.Pins {
		if p.Index != i {
			t.Errorf("pin %d has Index %d, want %d", i, p.Index, i)
		}
	}
}
