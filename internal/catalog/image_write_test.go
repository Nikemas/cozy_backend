package catalog

import (
	"context"
	"testing"
)

func TestReplaceForProductRejectsBlankObjectKey(t *testing.T) {
	repo := NewImageRepo(nil)

	_, err := repo.ReplaceForProduct(context.Background(), "product-1", []ImageInput{
		{ObjectKey: "products/product-1/a.jpg", SortOrder: 0},
		{ObjectKey: "  ", SortOrder: 1},
	})

	assertAppErrStatus(t, err, 400)
}
