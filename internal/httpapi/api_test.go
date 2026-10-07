package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"privacyfilter/filter"
)

func TestRedactStableTypesAndConfiguredLabels(t *testing.T) {
	f, err := filter.New("", filter.Config{
		Replacement:       "<private>",
		ReplacementLabels: map[string]string{"email": "<mail>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	Handler(f).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/redact", strings.NewReader(`{"text":"邮箱 a@b.com 手机 13812345678"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var res redactResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Redacted != "邮箱 <mail> 手机 <private>" || !res.Hit || res.Count != 2 {
		t.Errorf("unexpected response: %+v", res)
	}
	if len(res.Entities) != 2 || res.Entities[0].Type != "email" || res.Entities[1].Type != "phone" {
		t.Errorf("types must be stable IDs: %+v", res.Entities)
	}
	if strings.Contains(w.Body.String(), `"replacement"`) {
		t.Errorf("unexpected replacement output field: %s", w.Body.String())
	}
}

func TestNoHitEntitiesJSON(t *testing.T) {
	f, err := filter.New("", filter.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path, body string
		batch      bool
	}{
		{"/redact", `{"text":"你好🙂 hello world"}`, false},
		{"/redact/batch", `{"texts":["你好🙂 hello world",""]}`, true},
	} {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			Handler(f).ServeHTTP(w, httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body)))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var results []redactResponse
			if tt.batch {
				if err := json.Unmarshal(w.Body.Bytes(), &results); err != nil {
					t.Fatal(err)
				}
				if len(results) != 2 {
					t.Fatalf("batch result count = %d", len(results))
				}
			} else {
				var res redactResponse
				if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
					t.Fatal(err)
				}
				results = append(results, res)
			}
			for _, res := range results {
				if res.Hit || res.Count != 0 || res.Entities == nil || len(res.Entities) != 0 {
					t.Errorf("unexpected no-hit response: %+v", res)
				}
			}
		})
	}
}
