package points

import (
	"context"
	"net/http"
	"testing"
)

func TestPointInputNormalizesEmptyKindToBranch(t *testing.T) {
	in := PointInput{Name: "Т", City: "Бишкек", Address: "ул. Чуй, 1"}

	got, err := in.normalized()

	if err != nil {
		t.Fatalf("normalized() error = %v", err)
	}
	if got.Kind != KindBranch {
		t.Errorf("Kind = %q, want %q", got.Kind, KindBranch)
	}
}

func TestPointInputAcceptsWarehouseKind(t *testing.T) {
	in := PointInput{Name: "Склад", City: "Бишкек", Address: "ул. Складская, 5", Kind: " Warehouse "}

	got, err := in.normalized()

	if err != nil {
		t.Fatalf("normalized() error = %v", err)
	}
	if got.Kind != KindWarehouse {
		t.Errorf("Kind = %q, want %q", got.Kind, KindWarehouse)
	}
}

func TestPointCreateRejectsUnknownKind(t *testing.T) {
	repo := NewPointsRepo(nil)

	_, err := repo.Create(context.Background(), PointInput{Name: "Т", City: "Бишкек", Address: "ул. Чуй, 1", Kind: "shop"})

	assertAppErrStatus(t, err, http.StatusBadRequest)
}

func TestNormalizedDoesNotMutateInput(t *testing.T) {
	in := PointInput{Name: "Т", City: "Бишкек", Address: "ул. Чуй, 1"}

	_, _ = in.normalized()

	if in.Kind != "" {
		t.Errorf("input Kind mutated to %q", in.Kind)
	}
}

func TestUpdateRequestKeepsCurrentKindWhenOmitted(t *testing.T) {
	cases := []struct {
		name    string
		kind    *string
		current string
		want    string
	}{
		{"omitted keeps warehouse", nil, KindWarehouse, KindWarehouse},
		{"explicit branch wins", strp(KindBranch), KindWarehouse, KindBranch},
		{"explicit warehouse wins", strp(KindWarehouse), KindBranch, KindWarehouse},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := updatePointRequest{Name: "Т", City: "Бишкек", Address: "ул. 1", Kind: c.kind}

			got := req.toInput(c.current)

			if got.Kind != c.want {
				t.Errorf("Kind = %q, want %q", got.Kind, c.want)
			}
		})
	}
}

func strp(s string) *string { return &s }
