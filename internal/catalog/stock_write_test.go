package catalog

import (
	"context"
	"testing"
)

func TestStockUpsertRejectsNegativeQuantity(t *testing.T) {
	repo := NewStockRepo(nil)

	_, err := repo.Upsert(context.Background(), "variant-1", "point-1", -1)

	assertAppErrStatus(t, err, 400)
}
