package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/google/uuid"
)

var (
	ErrInvalidPluginManifest = errors.New("plugin registry: invalid manifest")
	ErrPluginConflict        = errors.New("plugin registry: version conflict")
	ErrPluginRegistryMissing = errors.New("plugin registry: not found")
)

type PluginManifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	RenderType  string   `json:"render_type"`
	EntryPoint  string   `json:"entry_point"`
	IconURL     string   `json:"icon_url,omitempty"`
}
type PluginRegistryService interface {
	Register(context.Context, string, uuid.UUID) (*db.Plugin, error)
	GetByID(context.Context, uuid.UUID) (*db.Plugin, error)
	ListActive(context.Context) ([]db.Plugin, error)
	Versions(context.Context, string) ([]db.Plugin, error)
	Activate(context.Context, string, string, uuid.UUID) (*db.Plugin, error)
	Disable(context.Context, string, uuid.UUID) (*db.Plugin, error)
	Archive(context.Context, string, uuid.UUID) (*db.Plugin, error)
}
type PluginRegistryServiceImpl struct{ repo db.PluginRepo }

func NewPluginRegistryService(r db.PluginRepo) *PluginRegistryServiceImpl {
	return &PluginRegistryServiceImpl{repo: r}
}

var pluginSemver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$`)
var canonicalPluginPermissions = map[string]struct{}{"data_read": {}, "data_write": {}, "notification": {}, "calendar_read": {}, "calendar_write": {}, "network_request": {}}

func parseRegistryManifest(source string) (PluginManifest, json.RawMessage, error) {
	const open = "/*@@canopy.manifest@@"
	const close = "@@end@@*/"
	i := strings.Index(source, open)
	if i < 0 {
		return PluginManifest{}, nil, ErrInvalidPluginManifest
	}
	raw := source[i+len(open):]
	j := strings.Index(raw, close)
	if j < 0 {
		return PluginManifest{}, nil, ErrInvalidPluginManifest
	}
	b := json.RawMessage(strings.TrimSpace(raw[:j]))
	var m PluginManifest
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e := d.Decode(&m); e != nil {
		return m, nil, fmt.Errorf("%w: %v", ErrInvalidPluginManifest, e)
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 100 || strings.TrimSpace(m.Description) == "" || len(m.Description) > 1000 || strings.TrimSpace(m.EntryPoint) == "" || !pluginSemver.MatchString(m.Version) {
		return m, nil, ErrInvalidPluginManifest
	}
	if m.RenderType != "card" && m.RenderType != "embed" && m.RenderType != "background" {
		return m, nil, ErrInvalidPluginManifest
	}
	for _, p := range m.Permissions {
		if _, ok := canonicalPluginPermissions[p]; !ok {
			return m, nil, fmt.Errorf("%w: unknown permission %q", ErrInvalidPluginManifest, p)
		}
	}
	return m, b, nil
}
func registrySlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if (r == ' ' || r == '-' || r == '_') && b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func (s *PluginRegistryServiceImpl) Register(c context.Context, source string, actor uuid.UUID) (*db.Plugin, error) {
	if len([]byte(source)) >= 1_048_576 {
		return nil, ErrInvalidPluginManifest
	}
	m, raw, e := parseRegistryManifest(source)
	if e != nil {
		return nil, e
	}
	slug := registrySlug(m.Name)
	if slug == "" {
		return nil, ErrInvalidPluginManifest
	}
	sum := sha256.Sum256([]byte(source))
	digest := hex.EncodeToString(sum[:])
	if _, e = hex.DecodeString(digest); e != nil {
		return nil, ErrInvalidPluginManifest
	}
	p, e := s.repo.Register(c, &db.Plugin{Name: m.Name, Slug: slug, Version: m.Version, Description: m.Description, AuthorProfileID: actor, Permissions: m.Permissions, ManifestJSON: raw, SourceJS: source, SourceSHA256: digest, SourceByteSize: len([]byte(source)), IconURL: m.IconURL})
	if errors.Is(e, db.ErrPluginDuplicate) {
		return nil, ErrPluginConflict
	}
	if e != nil {
		return nil, e
	}
	if e = s.repo.Audit(c, p.ID, "registered", actor, map[string]any{"name": p.Name, "version": p.Version}); e != nil {
		return nil, e
	}
	return p, nil
}
func (s *PluginRegistryServiceImpl) GetByID(c context.Context, id uuid.UUID) (*db.Plugin, error) {
	p, e := s.repo.GetByID(c, id)
	if errors.Is(e, db.ErrPluginNotFound) {
		e = ErrPluginRegistryMissing
	}
	return p, e
}
func (s *PluginRegistryServiceImpl) ListActive(c context.Context) ([]db.Plugin, error) {
	return s.repo.ListActive(c)
}
func (s *PluginRegistryServiceImpl) Versions(c context.Context, n string) ([]db.Plugin, error) {
	return s.repo.Versions(c, n)
}
func (s *PluginRegistryServiceImpl) Activate(c context.Context, n, v string, a uuid.UUID) (*db.Plugin, error) {
	vs, e := s.repo.Versions(c, n)
	if e != nil {
		return nil, e
	}
	for _, x := range vs {
		if x.Version == v {
			p, e := s.repo.Activate(c, n, x.ID)
			if e == nil {
				e = s.repo.Audit(c, p.ID, "rolled_back", a, map[string]any{"version": v})
			}
			return p, e
		}
	}
	return nil, ErrPluginRegistryMissing
}
func (s *PluginRegistryServiceImpl) Disable(c context.Context, n string, a uuid.UUID) (*db.Plugin, error) {
	p, e := s.repo.Disable(c, n)
	if e == nil {
		e = s.repo.Audit(c, p.ID, "paused", a, nil)
	}
	return p, e
}
func (s *PluginRegistryServiceImpl) Archive(c context.Context, n string, a uuid.UUID) (*db.Plugin, error) {
	p, e := s.repo.Archive(c, n)
	if e == nil {
		e = s.repo.Audit(c, p.ID, "uninstalled", a, nil)
	}
	return p, e
}
