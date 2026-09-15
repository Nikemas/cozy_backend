package catalog

import "testing"

func strPtr(s string) *string { return &s }

func TestBuildTreeNestsByParentID(t *testing.T) {
	flat := []*Category{
		{ID: "root-1", ParentID: nil, Slug: "shoes"},
		{ID: "root-2", ParentID: nil, Slug: "accessories"},
		{ID: "child-1", ParentID: strPtr("root-1"), Slug: "sneakers"},
		{ID: "child-2", ParentID: strPtr("root-1"), Slug: "boots"},
		{ID: "grandchild-1", ParentID: strPtr("child-1"), Slug: "running"},
	}

	roots := buildTree(flat)

	if len(roots) != 2 {
		t.Fatalf("got %d roots, want 2", len(roots))
	}
	if roots[0].ID != "root-1" || roots[1].ID != "root-2" {
		t.Fatalf("roots not in expected order: %+v", roots)
	}

	root1 := roots[0]
	if len(root1.Children) != 2 {
		t.Fatalf("root-1: got %d children, want 2", len(root1.Children))
	}
	if root1.Children[0].ID != "child-1" || root1.Children[1].ID != "child-2" {
		t.Fatalf("root-1 children not in expected order: %+v", root1.Children)
	}

	child1 := root1.Children[0]
	if len(child1.Children) != 1 || child1.Children[0].ID != "grandchild-1" {
		t.Fatalf("child-1: got children %+v, want [grandchild-1]", child1.Children)
	}

	root2 := roots[1]
	if len(root2.Children) != 0 {
		t.Fatalf("root-2: got %d children, want 0", len(root2.Children))
	}
}

func TestBuildTreeEmpty(t *testing.T) {
	roots := buildTree(nil)
	if len(roots) != 0 {
		t.Fatalf("got %d roots for empty input, want 0", len(roots))
	}
	// GET /api/v1/categories must serialize an empty tree as `[]`, not
	// `null` — the API contract is an array of categories.
	if roots == nil {
		t.Fatal("buildTree(nil) returned a nil slice, want a non-nil empty slice so it JSON-marshals as [] not null")
	}
}

func TestBuildTreeDanglingParentTreatedAsRoot(t *testing.T) {
	flat := []*Category{
		{ID: "orphan", ParentID: strPtr("does-not-exist"), Slug: "orphan"},
	}

	roots := buildTree(flat)

	if len(roots) != 1 || roots[0].ID != "orphan" {
		t.Fatalf("got %+v, want orphan surfaced as a root", roots)
	}
}

func TestResolveIDUUIDPatternMatchesUUID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"550e8400-e29b-41d4-a716-446655440000", true},
		{"550E8400-E29B-41D4-A716-446655440000", true},
		{"sneakers", false},
		{"", false},
		{"550e8400-e29b-41d4-a716", false},
	}

	for _, c := range cases {
		if got := uuidPattern.MatchString(c.in); got != c.want {
			t.Errorf("uuidPattern.MatchString(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
