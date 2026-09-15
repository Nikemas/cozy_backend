package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func assertAppErrStatus(t *testing.T, err error, status int) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *apperr.AppError, got %T (%v)", err, err)
	}
	if appErr.Status != status {
		t.Errorf("Status = %d, want %d (code=%s, msg=%s)", appErr.Status, status, appErr.Code, appErr.Message)
	}
}

func TestCategoryCreateValidatesRequiredFields(t *testing.T) {
	repo := NewCategoryRepo(nil)

	cases := []struct {
		name string
		in   CategoryInput
	}{
		{"missing name_ru", CategoryInput{NameKy: "аяккап", Slug: "shoes"}},
		{"missing name_ky", CategoryInput{NameRu: "обувь", Slug: "shoes"}},
		{"missing slug", CategoryInput{NameRu: "обувь", NameKy: "аяккап"}},
		{"blank name_ru", CategoryInput{NameRu: "   ", NameKy: "аяккап", Slug: "shoes"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := repo.Create(context.Background(), c.in)
			assertAppErrStatus(t, err, 400)
		})
	}
}

func TestCategoryUpdateRejectsSelfAsParent(t *testing.T) {
	repo := NewCategoryRepo(nil)
	id := "cat-1"

	_, err := repo.Update(context.Background(), id, CategoryInput{
		ParentID: &id,
		NameRu:   "обувь",
		NameKy:   "аяккап",
		Slug:     "shoes",
	})

	assertAppErrStatus(t, err, 400)
}

func TestCategoryUpdateValidatesRequiredFields(t *testing.T) {
	repo := NewCategoryRepo(nil)

	_, err := repo.Update(context.Background(), "cat-1", CategoryInput{NameRu: "обувь"})

	assertAppErrStatus(t, err, 400)
}
