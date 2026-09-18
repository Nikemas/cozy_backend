package apperr

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestInternal_DoesNotLeakRawErrorMessage(t *testing.T) {
	rawErr := errors.New("pq: connection to server at \"10.0.0.5\", port 5432 failed: password authentication failed for user \"cozy\"")

	appErr := Internal(rawErr)

	if appErr.Status != http.StatusInternalServerError {
		t.Fatalf("Status = %d, want %d", appErr.Status, http.StatusInternalServerError)
	}
	if appErr.Message == rawErr.Error() {
		t.Fatalf("Message leaks raw error text: %q", appErr.Message)
	}
	if strings.Contains(appErr.Message, "10.0.0.5") || strings.Contains(appErr.Message, "password authentication") {
		t.Fatalf("Message contains internal error detail: %q", appErr.Message)
	}
}
