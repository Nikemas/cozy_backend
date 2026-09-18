package config

import "testing"

func TestPublicObjectURL_UsesPublicEndpoint(t *testing.T) {
	cfg := &Config{
		MinIOEndpoint: "minio:9000", MinIOUseSSL: false,
		MinIOPublicEndpoint: "media.cozy.example.com", MinIOPublicUseSSL: true,
		MinIOBucket: "cozy-media",
	}
	got := cfg.PublicObjectURL("products/a.jpg")
	want := "https://media.cozy.example.com/cozy-media/products/a.jpg"
	if got != want {
		t.Fatalf("PublicObjectURL = %q, want %q", got, want)
	}
}

func TestPublicObjectURL_FallsBackToInternalEndpoint(t *testing.T) {
	cfg := &Config{MinIOEndpoint: "localhost:9000", MinIOBucket: "cozy-media"}
	if got, want := cfg.PublicObjectURL("k.jpg"), "http://localhost:9000/cozy-media/k.jpg"; got != want {
		t.Fatalf("PublicObjectURL = %q, want %q", got, want)
	}
	if got := cfg.PublicObjectURL(""); got != "" {
		t.Fatalf("empty key should give empty URL, got %q", got)
	}
}
