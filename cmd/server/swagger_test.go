package main

import (
	"context"
	"encoding/json"
	"go.uber.org/zap"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/go-chi/chi/v5"

	"github.com/shigabutdinoff/secunda/api"
)

// TestSwaggerMatchesRoutes ловит маршрут без аннотации и аннотацию без маршрута.
func TestSwaggerMatchesRoutes(t *testing.T) {
	var v2 openapi2.T
	if err := json.Unmarshal(api.Swagger, &v2); err != nil {
		t.Fatal(err)
	}
	doc, err := openapi2conv.ToV3(&v2)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal("описание API не прошло проверку OpenAPI: ", err)
	}
	var described []string
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			described = append(described, method+" "+path)
		}
	}
	var routed []string
	err = chi.Walk(router(nil, nil, "test-secret", zap.NewNop()), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route != "/swagger/doc.json" {
			routed = append(routed, method+" "+strings.TrimSuffix(route, "/"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(described)
	slices.Sort(routed)
	if !slices.Equal(described, routed) {
		t.Fatalf("описание %v не совпадает с маршрутами %v, запустите go generate ./internal/httpapi", described, routed)
	}
}

func TestSwaggerServed(t *testing.T) {
	rec := httptest.NewRecorder()
	router(nil, nil, "test-secret", zap.NewNop()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/swagger/doc.json", nil))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || string(body) != string(api.Swagger) {
		t.Fatalf("код %d, тип %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
