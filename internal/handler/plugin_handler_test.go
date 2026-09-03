package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type registryStub struct {
	rows   []db.Plugin
	audits int
}

func (s *registryStub) Register(_ context.Context, p *db.Plugin) (*db.Plugin, error) {
	for _, x := range s.rows {
		if x.Name == p.Name && x.Version == p.Version {
			return nil, db.ErrPluginDuplicate
		}
	}
	p.ID = uuid.New()
	p.Status = "active"
	p.IsRootVersion = len(s.rows) == 0
	for i := range s.rows {
		if s.rows[i].Name == p.Name && s.rows[i].Status == "active" {
			s.rows[i].Status = "archived"
			p.PreviousVersionID = &s.rows[i].ID
		}
	}
	s.rows = append(s.rows, *p)
	return p, nil
}
func (s *registryStub) GetByID(_ context.Context, id uuid.UUID) (*db.Plugin, error) {
	for i := range s.rows {
		if s.rows[i].ID == id {
			return &s.rows[i], nil
		}
	}
	return nil, db.ErrPluginNotFound
}
func (s *registryStub) ListActive(context.Context) ([]db.Plugin, error) {
	var o []db.Plugin
	for _, p := range s.rows {
		if p.Status == "active" {
			o = append(o, p)
		}
	}
	return o, nil
}
func (s *registryStub) Versions(_ context.Context, n string) ([]db.Plugin, error) {
	var o []db.Plugin
	for _, p := range s.rows {
		if p.Name == n {
			o = append(o, p)
		}
	}
	return o, nil
}
func (s *registryStub) Activate(_ context.Context, n string, id uuid.UUID) (*db.Plugin, error) {
	for i := range s.rows {
		if s.rows[i].Name == n && s.rows[i].Status == "active" {
			s.rows[i].Status = "archived"
		}
		if s.rows[i].ID == id {
			s.rows[i].Status = "active"
		}
	}
	return s.GetByID(context.Background(), id)
}
func (s *registryStub) Disable(_ context.Context, n string) (*db.Plugin, error) {
	return s.status(n, "disabled")
}
func (s *registryStub) Archive(_ context.Context, n string) (*db.Plugin, error) {
	return s.status(n, "archived")
}
func (s *registryStub) Rollback(context.Context, string) (*db.Plugin, error) {
	return nil, errors.New("unused")
}
func (s *registryStub) Audit(context.Context, uuid.UUID, string, uuid.UUID, map[string]any) error {
	s.audits++
	return nil
}
func (s *registryStub) Update(_ context.Context, p *db.Plugin, _ uuid.UUID) (*db.Plugin, error) {
	return s.Register(context.Background(), p)
}
func (s *registryStub) RollbackTo(ctx context.Context, n, v string, _ uuid.UUID) (*db.Plugin, error) {
	for _, p := range s.rows {
		if p.Slug == n && p.Version == v {
			return s.Activate(ctx, p.Name, p.ID)
		}
	}
	return nil, db.ErrPluginNotFound
}
func (s *registryStub) status(n, v string) (*db.Plugin, error) {
	for i := range s.rows {
		if s.rows[i].Name == n && s.rows[i].Status == "active" {
			s.rows[i].Status = v
			return &s.rows[i], nil
		}
	}
	return nil, db.ErrPluginNotFound
}

func pluginSource(name, version, permission string) string {
	return `/*@@canopy.manifest@@ {"name":"` + name + `","version":"` + version + `","description":"test","permissions":["` + permission + `"],"render_type":"card","entry_point":"main"} @@end@@*/ function main(){}`
}
func pluginReq(t *testing.T, h *PluginHandler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Mount("/api/v1/plugins", h.Routes())
	q := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	q = q.WithContext(context.WithValue(q.Context(), userIDContextKey{}, uuid.New()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	return w
}
func pluginBody(src string) string {
	b, _ := json.Marshal(map[string]string{"source_js": src})
	return string(b)
}

func TestPluginRegistryRegistrationScenarios(t *testing.T) {
	cases := []struct {
		name, src string
		want      int
	}{{"valid", pluginSource("viewer", "1.0.0", "data_read"), 201}, {"malformed", "/*@@canopy.manifest@@ {bad} @@end@@*/", 400}, {"oversize", pluginSource("viewer", "1.0.0", "data_read") + strings.Repeat("x", 1_048_576), 400}, {"semver", pluginSource("viewer", "1.2", "data_read"), 400}, {"permission", pluginSource("viewer", "1.0.0", "quantum_compute"), 400}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &registryStub{}
			w := pluginReq(t, NewPluginHandler(service.NewPluginRegistryService(repo)), http.MethodPost, "/api/v1/plugins/", pluginBody(tc.src))
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.want == 201 && (!repo.rows[0].IsRootVersion || repo.audits != 1) {
				t.Fatalf("root=%v audits=%d", repo.rows[0].IsRootVersion, repo.audits)
			}
		})
	}
	repo := &registryStub{}
	h := NewPluginHandler(service.NewPluginRegistryService(repo))
	body := pluginBody(pluginSource("dup", "1.0.0", "data_read"))
	pluginReq(t, h, http.MethodPost, "/api/v1/plugins/", body)
	if w := pluginReq(t, h, http.MethodPost, "/api/v1/plugins/", body); w.Code != 409 {
		t.Fatalf("duplicate=%d", w.Code)
	}
}
func TestPluginRegistryRollbackAndArchive(t *testing.T) {
	repo := &registryStub{}
	h := NewPluginHandler(service.NewPluginRegistryService(repo))
	for _, v := range []string{"1.0.0", "2.0.0"} {
		if w := pluginReq(t, h, http.MethodPost, "/api/v1/plugins/", pluginBody(pluginSource("viewer", v, "data_read"))); w.Code != 201 {
			t.Fatal(w.Body.String())
		}
	}
	if w := pluginReq(t, h, http.MethodPost, "/api/v1/plugins/viewer/activate", `{"version":"1.0.0"}`); w.Code != 200 || repo.rows[0].Status != "active" {
		t.Fatalf("rollback=%d", w.Code)
	}
	if w := pluginReq(t, h, http.MethodPost, "/api/v1/plugins/viewer/archive", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	active, _ := repo.ListActive(context.Background())
	if len(active) != 0 {
		t.Fatalf("active=%d", len(active))
	}
}

func TestPluginUpdateSourceAndRollbackNotFound(t *testing.T) {
	repo := &registryStub{}
	h := NewPluginHandler(service.NewPluginRegistryService(repo))
	if w := pluginReq(t, h, http.MethodPost, "/api/v1/plugins/", pluginBody(pluginSource("viewer", "1.0.0", "data_read"))); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	actor := uuid.New()
	source := pluginSource("viewer", "2.0.0", "data_read")
	body, _ := json.Marshal(map[string]any{"source_js": source, "actor_profile_id": actor})
	w := pluginReq(t, h, http.MethodPost, "/api/v1/plugins/viewer/update", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}
	active := repo.rows[len(repo.rows)-1]
	w = pluginReq(t, h, http.MethodGet, "/api/v1/plugins/"+active.ID.String()+"/source", "")
	if w.Code != http.StatusOK || w.Body.String() != source || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/javascript" {
		t.Fatalf("source status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	body, _ = json.Marshal(map[string]any{"target_version": "9.9.9", "actor_profile_id": actor})
	w = pluginReq(t, h, http.MethodPost, "/api/v1/plugins/viewer/rollback", string(body))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "PLUGIN_VERSION_NOT_FOUND") {
		t.Fatalf("rollback status=%d body=%s", w.Code, w.Body.String())
	}
}
