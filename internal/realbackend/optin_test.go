package realbackend

import (
	"net/http/httptest"
	"testing"
)

func TestWantsRealViaQueryParam(t *testing.T) {
	r := httptest.NewRequest("POST", "/whatever?backend=real", nil)
	if !WantsReal(r, nil) {
		t.Fatal("expected query param backend=real to opt in")
	}
}

func TestWantsRealViaLabel(t *testing.T) {
	r := httptest.NewRequest("POST", "/whatever", nil)
	if !WantsReal(r, map[string]string{"emulator.dev/backend": "real"}) {
		t.Fatal("expected label opt-in to match")
	}
}

func TestWantsRealDefaultsFalse(t *testing.T) {
	r := httptest.NewRequest("POST", "/whatever", nil)
	if WantsReal(r, nil) {
		t.Fatal("expected no opt-in by default")
	}
	if WantsReal(r, map[string]string{"emulator.dev/backend": "shape"}) {
		t.Fatal("expected non-'real' label value to not opt in")
	}
	if WantsReal(nil, nil) {
		t.Fatal("expected nil request + nil labels to not opt in")
	}
}
