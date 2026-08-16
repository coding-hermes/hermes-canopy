package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/plugin"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// --- Stub plugin service --------------------------------------------------

type stubPluginService struct {
	registerErr error
	registerOut *plugin.Plugin
	getErr      error
	getOut      *plugin.Plugin
	installErr  error
	installOut  *plugin.PluginInstance
	sourceOut   string
	sourceSHA   string
	sourceErr   error
}

func newStubPluginService() *stubPluginService {
	return &stubPluginService{
		registerOut: &plugin.Plugin{ID: uuid.New(), Name: "csv-viewer", Version: "1.0.0"},
		installOut:  &plugin.PluginInstance{ID: uuid.New(), Status: "active"},
	}
}

func (s *stubPluginService) Register(_ context.Context, _ plugin.PluginManifest, _ string, _ uuid.UUID) (*plugin.Plugin, error) {
	if s.registerErr != nil {
		return nil, s.registerErr
	}
	return s.registerOut, nil
}
func (s *stubPluginService) Get(_ context.Context, _ uuid.UUID) (*plugin.Plugin, error) {
	return s.getOut, s.getErr
}
func (s *stubPluginService) List(_ context.Context, _, _ int) ([]plugin.Plugin, int, error) {
	return nil, 0, nil
}
func (s *stubPluginService) Install(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ uuid.UUID, _ []plugin.Permission) (*plugin.PluginInstance, error) {
	return s.installOut, s.installErr
}
func (s *stubPluginService) CheckPermission(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}
func (s *stubPluginService) GetSource(_ context.Context, _ uuid.UUID) (string, string, error) {
	return s.sourceOut, s.sourceSHA, s.sourceErr
}
func (s *stubPluginService) ListInstances(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]plugin.PluginInstance, error) {
	return nil, nil
}
func (s *stubPluginService) PauseInstance(_ context.Context, _ uuid.UUID) error { return nil }
func (s *stubPluginService) ResumeInstance(_ context.Context, _ uuid.UUID) error {
	return nil
}

// --- Test harness ---------------------------------------------------------

const pluginTestSecret = "plugin-test-secret"

func pluginTestToken(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID.String(),
	})
	raw, err := token.SignedString([]byte(pluginTestSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return raw
}

// newPluginTestRouter builds a chi router with auth middleware + the plugin
// routes, mirroring the server.go wiring (GAP-002 §4.2).
func newPluginTestRouter(t *testing.T, svc plugin.Service) *chi.Mux {
	t.Helper()
	r := chi.NewRouter()
	r.Use(AuthMiddleware(pluginTestSecret))
	r.Mount("/api/v1/plugins", NewPluginHandler(svc).Routes())
	return r
}

func doJSON(t *testing.T, r *chi.Mux, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func validSource() string {
	return `/**
 * @canopy-manifest
 * {"name": "csv-viewer", "version": "1.0.0", "description": "CSV cards",
 *  "permissions": ["data_read"], "render_type": "card", "entry_point": "main"}
 * @end-canopy-manifest
 */
function main() {}`
}

// --- Handler scenarios (GAP-002 §8) ----------------------------------------

// register 201.
func TestPluginHandlerRegister201(t *testing.T) {
	svc := newStubPluginService()
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	body := fmt.Sprintf(`{"source": %q}`, validSource())
	w := doJSON(t, router, http.MethodPost, "/api/v1/plugins/register", token, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Plugin plugin.Plugin `json:"plugin"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Plugin.ID != svc.registerOut.ID {
		t.Errorf("plugin id = %s, want %s", resp.Plugin.ID, svc.registerOut.ID)
	}
}

// register 400 bad manifest.
func TestPluginHandlerRegister400BadManifest(t *testing.T) {
	svc := newStubPluginService()
	svc.registerErr = plugin.ErrInvalidManifest
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	body := `{"source": "function main() {}"}`
	w := doJSON(t, router, http.MethodPost, "/api/v1/plugins/register", token, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVALID_MANIFEST") {
		t.Errorf("body = %s, want INVALID_MANIFEST code", w.Body.String())
	}
}

// register 413 oversized.
func TestPluginHandlerRegister413(t *testing.T) {
	svc := newStubPluginService()
	svc.registerErr = plugin.ErrPluginTooLarge
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	body := fmt.Sprintf(`{"source": %q}`, validSource())
	w := doJSON(t, router, http.MethodPost, "/api/v1/plugins/register", token, body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PLUGIN_TOO_LARGE") {
		t.Errorf("body = %s, want PLUGIN_TOO_LARGE code", w.Body.String())
	}
}

// install 201.
func TestPluginHandlerInstall201(t *testing.T) {
	svc := newStubPluginService()
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())
	pluginID := uuid.New()
	treeID := uuid.New()

	body := fmt.Sprintf(`{"treeId": %q, "grantedPermissions": ["data_read"]}`, treeID.String())
	w := doJSON(t, router, http.MethodPost, "/api/v1/plugins/"+pluginID.String()+"/install", token, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Instance plugin.PluginInstance `json:"instance"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Instance.ID != svc.installOut.ID {
		t.Errorf("instance id = %s, want %s", resp.Instance.ID, svc.installOut.ID)
	}
}

// install 409 duplicate.
func TestPluginHandlerInstall409(t *testing.T) {
	svc := newStubPluginService()
	svc.installErr = plugin.ErrAlreadyInstalled
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	body := `{"grantedPermissions": ["data_read"]}`
	w := doJSON(t, router, http.MethodPost, "/api/v1/plugins/"+uuid.New().String()+"/install", token, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PLUGIN_ALREADY_INSTALLED") {
		t.Errorf("body = %s, want PLUGIN_ALREADY_INSTALLED code", w.Body.String())
	}
}

// get 404 unknown.
func TestPluginHandlerGet404(t *testing.T) {
	svc := newStubPluginService()
	svc.getErr = plugin.ErrPluginNotFound
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	w := doJSON(t, router, http.MethodGet, "/api/v1/plugins/"+uuid.New().String(), token, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PLUGIN_NOT_FOUND") {
		t.Errorf("body = %s, want PLUGIN_NOT_FOUND code", w.Body.String())
	}
}

// source 200 + sha header.
func TestPluginHandlerSource200(t *testing.T) {
	svc := newStubPluginService()
	svc.sourceOut = validSource()
	svc.sourceSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	w := doJSON(t, router, http.MethodGet, "/api/v1/plugins/"+uuid.New().String()+"/source", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Source-SHA256"); got != svc.sourceSHA {
		t.Errorf("X-Source-SHA256 = %q, want %q", got, svc.sourceSHA)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript", ct)
	}
	if !strings.Contains(w.Body.String(), "@canopy-manifest") {
		t.Error("body does not contain plugin source")
	}
}

// unauth 401.
func TestPluginHandlerUnauth401(t *testing.T) {
	router := newPluginTestRouter(t, newStubPluginService())
	w := doJSON(t, router, http.MethodGet, "/api/v1/plugins", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// install 400 invalid treeId (defensive — route contract says valid UUID).
func TestPluginHandlerInstallInvalidTreeID(t *testing.T) {
	router := newPluginTestRouter(t, newStubPluginService())
	token := pluginTestToken(t, uuid.New())

	body := `{"treeId": "not-a-uuid", "grantedPermissions": []}`
	w := doJSON(t, router, http.MethodPost, "/api/v1/plugins/"+uuid.New().String()+"/install", token, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// register with a manifest validation failure maps to MANIFEST_VALIDATION_FAILED.
func TestPluginHandlerRegisterManifestValidation(t *testing.T) {
	svc := newStubPluginService()
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	body := `{"source": "/**\n * @canopy-manifest\n * {\"name\": \"x\", \"version\": \"1.2\"}\n * @end-canopy-manifest\n */\nfunction main() {}"}`
	w := doJSON(t, router, http.MethodPost, "/api/v1/plugins/register", token, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "MANIFEST_VALIDATION_FAILED") {
		t.Errorf("body = %s, want MANIFEST_VALIDATION_FAILED code", w.Body.String())
	}
}

// register with an unknown permission maps to 422 INVALID_PERMISSION.
func TestPluginHandlerRegister422(t *testing.T) {
	svc := newStubPluginService()
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	body := `{"source": "/**\n * @canopy-manifest\n * {\"name\": \"x\", \"version\": \"1.0.0\", \"permissions\": [\"quantum_compute\"],\n *  \"render_type\": \"card\", \"entry_point\": \"main\"}\n * @end-canopy-manifest\n */\nfunction main() {}"}`
	w := doJSON(t, router, http.MethodPost, "/api/v1/plugins/register", token, body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVALID_PERMISSION") {
		t.Errorf("body = %s, want INVALID_PERMISSION code", w.Body.String())
	}
}

// database unavailable → 503.
func TestPluginHandlerDBUnavailable503(t *testing.T) {
	svc := newStubPluginService()
	svc.getErr = fmt.Errorf("wrapped: %w", service.ErrDatabaseUnavailable)
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	w := doJSON(t, router, http.MethodGet, "/api/v1/plugins/"+uuid.New().String(), token, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "SERVICE_UNAVAILABLE") {
		t.Errorf("body = %s, want SERVICE_UNAVAILABLE code", w.Body.String())
	}
}

// unknown service error → generic 500 (BUG-020 pattern).
func TestPluginHandlerInternal500(t *testing.T) {
	svc := newStubPluginService()
	svc.getErr = errors.New("boom")
	router := newPluginTestRouter(t, svc)
	token := pluginTestToken(t, uuid.New())

	w := doJSON(t, router, http.MethodGet, "/api/v1/plugins/"+uuid.New().String(), token, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "boom") {
		t.Errorf("body leaks internal error: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "INTERNAL_ERROR") {
		t.Errorf("body = %s, want INTERNAL_ERROR code", w.Body.String())
	}
}
