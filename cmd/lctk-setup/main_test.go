package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWizardPageCarriesAUniqueLocalSessionAndRejectsForeignWrites(t *testing.T) {
	first, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 40 {
		t.Fatal("setup session tokens are not independently strong")
	}
	wizard := &wizard{token: first}
	page := httptest.NewRecorder()
	wizard.handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), first) {
		t.Fatalf("page status=%d", page.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/install", nil)
	request.Header.Set("X-LCTK-Setup", second)
	response := httptest.NewRecorder()
	wizard.handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("foreign write returned %d", response.Code)
	}
}
