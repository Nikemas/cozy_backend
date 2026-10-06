package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func testObjectURL(key string) string { return "https://media.example/cozy-media/" + key }

func TestWithImageURLsFillsTreeWithoutMutatingIt(t *testing.T) {
	child := &Category{ID: "c2", ParentID: strPtr("c1"), NameRu: "Мужские", ImageKey: strPtr("categories/m.jpg")}
	root := &Category{ID: "c1", NameRu: "Кроссовки", Children: []*Category{child}}
	other := &Category{ID: "c3", NameRu: "Сапоги", ImageKey: strPtr("categories/s.png")}
	tree := []*Category{root, other}

	out := WithImageURLs(tree, testObjectURL)

	if len(out) != 2 || out[0].ImageURL != nil {
		t.Fatalf("root without photo: %+v", out[0])
	}
	if got := out[0].Children[0].ImageURL; got == nil || *got != "https://media.example/cozy-media/categories/m.jpg" {
		t.Errorf("child image_url = %v", got)
	}
	if got := out[1].ImageURL; got == nil || *got != "https://media.example/cozy-media/categories/s.png" {
		t.Errorf("root image_url = %v", got)
	}
	if child.ImageURL != nil || other.ImageURL != nil || out[0] == root || out[0].Children[0] == child {
		t.Error("input tree was mutated or shared")
	}
}

func TestWithImageURLTreatsBlankKeyAsNoPhoto(t *testing.T) {
	c := Category{ID: "c1", ImageKey: strPtr("")}
	if got := c.WithImageURL(testObjectURL).ImageURL; got != nil {
		t.Errorf("image_url = %q, want nil", *got)
	}
}

func TestCategoryJSONExposesImageURLNotKey(t *testing.T) {
	without, err := json.Marshal(Category{ID: "c1", NameRu: "Кроссовки", ImageKey: strPtr("categories/x.jpg")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(without), `"image_url":null`) || strings.Contains(string(without), "image_key") ||
		strings.Contains(string(without), "categories/x.jpg") {
		t.Errorf("json = %s", without)
	}

	with, err := json.Marshal(Category{ID: "c1", ImageKey: strPtr("categories/x.jpg")}.WithImageURL(testObjectURL))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(with), `"image_url":"https://media.example/cozy-media/categories/x.jpg"`) {
		t.Errorf("json = %s", with)
	}
}
