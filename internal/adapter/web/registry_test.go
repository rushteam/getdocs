package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

func TestRegistryAPI(t *testing.T) {
	h, _ := newServerWith(t, map[string]string{"README.md": "# Home\n"}, docs.Filter{})
	auth := map[string]string{"Authorization": "Bearer admin"}
	post := func(body string, header map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/trees", strings.NewReader(body))
		for k, v := range header {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	cases := []struct {
		name   string
		body   string
		header map[string]string
		want   int
	}{
		{"no token", `{"namespace":"x","name":"y","repo":"https://github.com/a/b.git"}`, nil, http.StatusUnauthorized},
		{"local file", `{"namespace":"x","name":"y","repo":"file:///etc"}`, auth, http.StatusBadRequest},
		{"embedded credentials", `{"namespace":"x","name":"y","repo":"https://u:p@github.com/a/b.git"}`, auth, http.StatusBadRequest},
		{"private host", `{"namespace":"x","name":"y","repo":"https://127.0.0.1/a/b.git"}`, auth, http.StatusBadRequest},
		{"unknown field", `{"namespace":"x","name":"y","repo":"https://github.com/a/b.git","path":"/etc"}`, auth, http.StatusBadRequest},
	}
	for _, c := range cases {
		if w := post(c.body, c.header); w.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, w.Code, c.want, w.Body)
		}
	}

	if w := do(h, http.MethodDelete, "/api/v1/trees/demo/local", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("delete without token = %d", w.Code)
	}
	if w := do(h, http.MethodDelete, "/api/v1/trees/demo/local", auth); w.Code != http.StatusBadRequest {
		t.Errorf("delete config tree = %d, want 400", w.Code)
	}
	if w := do(h, http.MethodDelete, "/api/v1/trees/demo/none", auth); w.Code != http.StatusNotFound {
		t.Errorf("delete unknown tree = %d, want 404", w.Code)
	}
	if w := do(h, http.MethodGet, "/status", nil); !strings.Contains(w.Body.String(), `data-open="add-tree"`) {
		t.Error("status page has no add-tree button")
	}
}
