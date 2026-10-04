package web

import (
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/storefront"
)

func TestBranchCardRouteURLIs2GIS(t *testing.T) {
	lat, lng := 42.8746, 74.5698
	data := buildBranchesData([]storefront.Branch{
		{ID: "1", Name: "A", City: "Бишкек", Address: "ул. Ибраимова, 115", WorkingHours: "10:00–20:00", Latitude: &lat, Longitude: &lng},
		{ID: "2", Name: "B", City: "Бишкек", Address: "ул. Ибраимова, 115"},
	})
	if got, want := data.Cards[0].RouteURL, "https://2gis.kg/geo/74.569800,42.874600"; got != want {
		t.Errorf("with coords RouteURL = %q, want %q", got, want)
	}
	if got := data.Cards[1].RouteURL; !strings.HasPrefix(got, "https://2gis.kg/search/") || strings.Contains(got, " ") {
		t.Errorf("without coords RouteURL = %q, want escaped 2GIS search URL", got)
	}
	if data.Cards[0].Hours != "10:00–20:00" {
		t.Errorf("Hours = %q", data.Cards[0].Hours)
	}
}

func TestBuildBranchesDataEmpty(t *testing.T) {
	data := buildBranchesData(nil)
	if !data.Empty {
		t.Error("buildBranchesData(nil).Empty = false, want true")
	}
	if len(data.Cards) != 0 {
		t.Error("buildBranchesData(nil) should produce no cards")
	}
}

func TestBuildBranchesDataMarksFirstCard(t *testing.T) {
	branches := []storefront.Branch{
		{ID: "1", Name: "Cozy Bishkek Park", Address: "ул. Ибраимова, 115"},
		{ID: "2", Name: "Cozy Vefa Center", Address: "просп. Чуй, 219"},
	}
	data := buildBranchesData(branches)

	if data.Empty {
		t.Fatal("buildBranchesData(2 branches).Empty = true, want false")
	}
	if len(data.Cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(data.Cards))
	}
	if !data.Cards[0].FirstInSet || data.Cards[1].FirstInSet {
		t.Error("only the first card should have FirstInSet = true")
	}
}

func TestBranchCardEmbedURLUsesCoordsOrAddress(t *testing.T) {
	lat, lng := 42.8746, 74.5698
	data := buildBranchesData([]storefront.Branch{
		{ID: "1", Name: "A", City: "Бишкек", Address: "ул. Чуй, 1", Latitude: &lat, Longitude: &lng},
		{ID: "2", Name: "B", City: "Бишкек", Address: "ул. Чуй, 1"},
	})
	if got := data.Cards[0].EmbedURL; !strings.Contains(got, "output=embed") || !strings.Contains(got, "q=42.874600%2C74.569800") {
		t.Errorf("with coords EmbedURL = %q", got)
	}
	if got := data.Cards[1].EmbedURL; strings.Contains(got, " ") || !strings.Contains(got, "q=%D0%91") {
		t.Errorf("without coords EmbedURL = %q", got)
	}
}
