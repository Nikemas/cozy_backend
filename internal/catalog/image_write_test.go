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

func TestForColorPrefersImagesTaggedWithTheSelectedColor(t *testing.T) {
	images := []ProductImage{
		{ID: "1", ObjectKey: "general.jpg", Color: nil},
		{ID: "2", ObjectKey: "black.jpg", Color: strPtr("Черный")},
		{ID: "3", ObjectKey: "white.jpg", Color: strPtr("Белый")},
	}

	got := ForColor(images, "Черный")

	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("ForColor(images, %q) = %+v, want just the black-tagged image", "Черный", got)
	}
}

func TestForColorFallsBackToGeneralImagesWhenColorHasNone(t *testing.T) {
	images := []ProductImage{
		{ID: "1", ObjectKey: "general.jpg", Color: nil},
		{ID: "2", ObjectKey: "white.jpg", Color: strPtr("Белый")},
	}

	got := ForColor(images, "Черный")

	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("ForColor(images, %q) = %+v, want the general (untagged) image as fallback", "Черный", got)
	}
}

func TestForColorFallsBackToEverythingWhenNoGeneralOrMatchingColorImages(t *testing.T) {
	images := []ProductImage{
		{ID: "1", ObjectKey: "white.jpg", Color: strPtr("Белый")},
	}

	got := ForColor(images, "Черный")

	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("ForColor(images, %q) = %+v, want all images as last-resort fallback", "Черный", got)
	}
}
