package catalog

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSortedSizesNumericFirst(t *testing.T) {
	got := SortedSizes(map[string]bool{"42": true, "36,5": true, "M": true, "39": true, "L": true, "": true, "36.5": true, "40": true})
	want := []string{"36,5", "36.5", "39", "40", "42", "L", "M"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortedSizes = %v, want %v", got, want)
	}
}

func TestBuildFacetConditionsMatchesListScope(t *testing.T) {
	tests := []struct {
		name     string
		filter   FacetFilter
		wantSQL  []string
		wantArgs int
	}{
		{"no scope", FacetFilter{}, []string{"is_active = true"}, 0},
		{"single category", FacetFilter{CategoryID: "c1"}, []string{"category_id = $1"}, 1},
		{"category set wins", FacetFilter{CategoryID: "c1", CategoryIDs: []string{"a", "b"}}, []string{"category_id = ANY($1)"}, 1},
		{"search", FacetFilter{Query: "nike air"}, []string{"name_ru ILIKE $1", "name_ru ILIKE $2"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args := buildFacetConditions(tt.filter)
			if !strings.HasPrefix(where, "WHERE is_active = true") {
				t.Errorf("where = %q, want active-only prefix", where)
			}
			for _, frag := range tt.wantSQL {
				if !strings.Contains(where, frag) {
					t.Errorf("where = %q, missing %q", where, frag)
				}
			}
			if len(args) != tt.wantArgs {
				t.Errorf("args = %v, want %d", args, tt.wantArgs)
			}
		})
	}
}

func TestFacetsCollectsDistinctSortedValuesAndPriceRange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// One pass: (size, color) pairs with their price range; a product
	// without variants comes back as a NULL size/color row.
	mock.ExpectQuery(`SELECT pv.size, pv.color,\s+MIN\(COALESCE\(pv.price_override, products.base_price\)\),\s+MAX\(COALESCE\(pv.price_override, products.base_price\)\)\s+FROM products\s+LEFT JOIN product_variants pv ON pv.product_id = products.id\s+WHERE is_active = true AND category_id = \$1\s+GROUP BY pv.size, pv.color`).
		WithArgs("c1").
		WillReturnRows(sqlmock.NewRows([]string{"size", "color", "min", "max"}).
			AddRow("42", "Черный", 2000.0, 7000.0).
			AddRow("38", "Чёрный", 1800.0, 1800.0).
			AddRow("38", "белый", 2500.0, 2600.0).
			AddRow(nil, nil, 1500.0, 1500.0). // product without variants
			AddRow("", "", 3000.0, 3000.0))   // blank values never become options

	got, err := NewProductRepo(db).Facets(context.Background(), FacetFilter{CategoryID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"38", "42"}; !reflect.DeepEqual(got.Sizes, want) {
		t.Errorf("Sizes = %v, want %v", got.Sizes, want)
	}
	if want := []string{"Черный", "Чёрный", "белый"}; !reflect.DeepEqual(got.Colors, want) {
		t.Errorf("Colors = %v, want %v (distinct as stored)", got.Colors, want)
	}
	if got.PriceMin == nil || *got.PriceMin != 1500 || got.PriceMax == nil || *got.PriceMax != 7000 {
		t.Errorf("price range = %v..%v, want 1500..7000", got.PriceMin, got.PriceMax)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestFacetsEmptyScopeHasNoPriceRange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`SELECT pv.size, pv.color`).WillReturnRows(sqlmock.NewRows([]string{"size", "color", "min", "max"}))

	got, err := NewProductRepo(db).Facets(context.Background(), FacetFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sizes == nil || got.Colors == nil || len(got.Sizes)+len(got.Colors) != 0 {
		t.Errorf("want empty non-nil slices, got %+v", got)
	}
	if got.PriceMin != nil || got.PriceMax != nil {
		t.Errorf("want nil price range, got %v..%v", got.PriceMin, got.PriceMax)
	}
}

func TestSubtreeIDs(t *testing.T) {
	leaf := &Category{ID: "leaf"}
	mid := &Category{ID: "mid", Children: []*Category{leaf}}
	tree := []*Category{{ID: "top", Children: []*Category{mid, {ID: "sib"}}}, {ID: "other"}}

	tests := []struct {
		id   string
		want []string
	}{
		{"top", []string{"top", "mid", "leaf", "sib"}},
		{"mid", []string{"mid", "leaf"}},
		{"leaf", []string{"leaf"}},
		{"missing", []string{"missing"}},
	}
	for _, tt := range tests {
		if got := SubtreeIDs(tree, tt.id); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SubtreeIDs(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}
