package media

import (
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

func TestExtensionForContentTypeAcceptsImageTypes(t *testing.T) {
	cases := map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/webp": ".webp",
	}
	for contentType, wantExt := range cases {
		ext, err := extensionForContentType(contentType)
		if err != nil {
			t.Errorf("extensionForContentType(%q): unexpected error: %v", contentType, err)
			continue
		}
		if ext != wantExt {
			t.Errorf("extensionForContentType(%q) = %q, want %q", contentType, ext, wantExt)
		}
	}
}

func TestExtensionForContentTypeRejectsNonImageTypes(t *testing.T) {
	cases := []string{
		"text/html",
		"application/zip",
		"application/pdf",
		"image/svg+xml", // scriptable, deliberately not on the allow-list
		"",
		"IMAGE/JPEG", // case must match exactly, no normalization assumed
	}
	for _, contentType := range cases {
		_, err := extensionForContentType(contentType)
		if err == nil {
			t.Errorf("extensionForContentType(%q): expected an error, got nil", contentType)
			continue
		}
		var appErr *apperr.AppError
		if ae, ok := err.(*apperr.AppError); ok {
			appErr = ae
		}
		if appErr == nil {
			t.Errorf("extensionForContentType(%q): expected *apperr.AppError, got %T", contentType, err)
			continue
		}
		if appErr.Status != 400 {
			t.Errorf("extensionForContentType(%q): expected 400 status, got %d", contentType, appErr.Status)
		}
	}
}

func TestNewObjectKeyIsUniqueAndUsesRightExtension(t *testing.T) {
	key1, err := newObjectKey("image/png")
	if err != nil {
		t.Fatalf("newObjectKey: unexpected error: %v", err)
	}
	key2, err := newObjectKey("image/png")
	if err != nil {
		t.Fatalf("newObjectKey: unexpected error: %v", err)
	}

	if key1 == key2 {
		t.Fatalf("newObjectKey returned the same key twice: %q", key1)
	}
	if !strings.HasPrefix(key1, "products/") {
		t.Errorf("newObjectKey(%q) = %q, want products/ prefix", "image/png", key1)
	}
	if !strings.HasSuffix(key1, ".png") {
		t.Errorf("newObjectKey(%q) = %q, want .png suffix", "image/png", key1)
	}
}

func TestNewObjectKeyRejectsBadContentType(t *testing.T) {
	if _, err := newObjectKey("application/octet-stream"); err == nil {
		t.Fatal("newObjectKey: expected an error for a non-image content type")
	}
}
