package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// --- Stub repo (in-memory, no PG) -----------------------------------------

type stubRepo struct {
	mu        sync.Mutex
	byID      map[uuid.UUID]*Plugin
	byName    map[string]*Plugin // active plugin per name
	byNameVer map[string]*Plugin // any plugin per "name\x00version"
	order     []uuid.UUID        // insert order for List
	instances map[uuid.UUID]*PluginInstance
	audits    []PluginAuditEntry
}

func newStubRepo() *stubRepo {
	return &stubRepo{
		byID:      make(map[uuid.UUID]*Plugin),
		byName:    make(map[string]*Plugin),
		byNameVer: make(map[string]*Plugin),
		instances: make(map[uuid.UUID]*PluginInstance),
	}
}

func nameVersionKey(name, version string) string { return name + "\x00" + version }

func (s *stubRepo) Register(_ context.Context, p *Plugin) (*Plugin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byNameVer[nameVersionKey(p.Name, p.Version)]; ok {
		// Concurrent-register race path: return the stored row.
		return existing, nil
	}
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	p.CreatedAt = now()
	p.UpdatedAt = p.CreatedAt
	clone := *p
	s.byID[p.ID] = &clone
	s.byNameVer[nameVersionKey(p.Name, p.Version)] = &clone
	if p.Status == PluginStatusActive {
		s.byName[p.Name] = &clone
	}
	s.order = append(s.order, p.ID)
	return &clone, nil
}

func (s *stubRepo) GetByID(_ context.Context, id uuid.UUID) (*Plugin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return nil, ErrPluginNotFound
	}
	clone := *p
	return &clone, nil
}

func (s *stubRepo) GetActiveByName(_ context.Context, name string) (*Plugin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byName[name]
	if !ok {
		return nil, ErrPluginNotFound
	}
	clone := *p
	return &clone, nil
}

func (s *stubRepo) List(_ context.Context, limit, offset int) ([]Plugin, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := len(s.order)
	out := make([]Plugin, 0, limit)
	for i := len(s.order) - 1; i >= 0 && len(out) < limit; i-- {
		if offset > 0 {
			offset--
			continue
		}
		out = append(out, *s.byID[s.order[i]])
	}
	return out, total, nil
}

func (s *stubRepo) Install(_ context.Context, inst *PluginInstance) (*PluginInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.instances {
		if existing.Status == InstanceStatusUninstalled {
			continue
		}
		if existing.PluginID == inst.PluginID && existing.ProfileID == inst.ProfileID &&
			equalTreeID(existing.TreeID, inst.TreeID) {
			return nil, ErrAlreadyInstalled
		}
	}
	if inst.ID == uuid.Nil {
		inst.ID = uuid.New()
	}
	inst.CreatedAt = now()
	clone := *inst
	s.instances[inst.ID] = &clone
	if p, ok := s.byID[inst.PluginID]; ok {
		p.InstallCount++
	}
	return &clone, nil
}

func (s *stubRepo) GetInstance(_ context.Context, id uuid.UUID) (*PluginInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.instances[id]
	if !ok {
		return nil, ErrInstanceNotFound
	}
	clone := *inst
	return &clone, nil
}

func (s *stubRepo) ListInstances(_ context.Context, profileID uuid.UUID, treeID *uuid.UUID) ([]PluginInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PluginInstance
	for _, inst := range s.instances {
		if inst.ProfileID != profileID {
			continue
		}
		if treeID != nil && !equalTreeID(inst.TreeID, treeID) {
			continue
		}
		out = append(out, *inst)
	}
	return out, nil
}

func (s *stubRepo) UpdateInstanceStatus(_ context.Context, id uuid.UUID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.instances[id]
	if !ok {
		return ErrInstanceNotFound
	}
	inst.Status = status
	return nil
}

func (s *stubRepo) IncrementInvokeCount(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.instances[id]
	if !ok {
		return ErrInstanceNotFound
	}
	inst.InvokeCount++
	return nil
}

func (s *stubRepo) Archive(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return ErrPluginNotFound
	}
	p.Status = PluginStatusArchived
	ts := now()
	p.ArchivedAt = &ts
	if s.byName[p.Name] == p {
		delete(s.byName, p.Name)
	}
	return nil
}

// GetVersionByName returns any stored row for (name, version), any status.
func (s *stubRepo) GetVersionByName(_ context.Context, name, version string) (*Plugin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byNameVer[nameVersionKey(name, version)]
	if !ok {
		return nil, fmt.Errorf("%w: %s@%s", ErrPluginVersionNotFound, name, version)
	}
	clone := *p
	return &clone, nil
}

// ListVersionsByName returns all rows for a name, newest first (the stub
// assigns CreatedAt in Register order, so reverse registration order = DESC).
func (s *stubRepo) ListVersionsByName(_ context.Context, name string) ([]Plugin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Plugin
	for i := len(s.order) - 1; i >= 0; i-- {
		p := s.byID[s.order[i]]
		if p.Name == name {
			out = append(out, *p)
		}
	}
	return out, nil
}

// UpdateVersionChain archives oldID, links old→new and new→old.
func (s *stubRepo) UpdateVersionChain(_ context.Context, oldID, newID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldP, ok := s.byID[oldID]
	if !ok {
		return ErrPluginNotFound
	}
	newP, ok := s.byID[newID]
	if !ok {
		return ErrPluginNotFound
	}
	oldP.Status = PluginStatusArchived
	ts := now()
	oldP.ArchivedAt = &ts
	oldP.SupersededByID = &newID
	newP.PreviousVersionID = &oldID
	if s.byName[oldP.Name] == oldP {
		delete(s.byName, oldP.Name)
	}
	return nil
}

// ActivateVersion flips a stored row back to active, clears archived_at and
// records the row it replaced (superseded_by_id).
func (s *stubRepo) ActivateVersion(_ context.Context, targetID, supersedingID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[targetID]
	if !ok {
		return ErrPluginNotFound
	}
	p.Status = PluginStatusActive
	p.ArchivedAt = nil
	p.SupersededByID = &supersedingID
	s.byName[p.Name] = p
	return nil
}

func (s *stubRepo) Audit(_ context.Context, entry *PluginAuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, *entry)
	return nil
}

func equalTreeID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// now returns the current UTC time for stub timestamps.
func now() time.Time { return time.Now().UTC() }

// --- Test fixtures --------------------------------------------------------

const testManifestTemplate = `/**
 * @canopy-manifest
 * %s
 * @end-canopy-manifest
 */
function main() { return "hello"; }`

func manifestJSON(t *testing.T, m PluginManifest) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(raw)
}

// buildSource renders a full plugin source (manifest block + JS body),
// optionally padded to a minimum byte size.
func buildSource(t *testing.T, m PluginManifest, minBytes int) string {
	t.Helper()
	src := fmt.Sprintf(testManifestTemplate, manifestJSON(t, m))
	if minBytes > len(src) {
		src += strings.Repeat("// padding\n", (minBytes-len(src)+10)/10)
	}
	return src
}

func testManifest() PluginManifest {
	return PluginManifest{
		Name:        "csv-viewer",
		Version:     "1.0.0",
		Description: "View CSV cards",
		Permissions: []Permission{PermissionDataRead},
		RenderType:  RenderTypeCard,
		EntryPoint:  "main",
	}
}

var testAuthorID = uuid.MustParse("0191a8b2-7fff-7000-9000-000000000042")

// --- Scenario tests (GAP-002 §8, all 24) ---------------------------------

// 1: Register valid plugin (10KB JS + manifest).
func TestServiceRegisterValid(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	source := buildSource(t, testManifest(), 10240)

	p, err := svc.Register(context.Background(), testManifest(), source, testAuthorID)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if p.Name != "csv-viewer" || p.Version != "1.0.0" {
		t.Errorf("unexpected plugin: %s@%s", p.Name, p.Version)
	}
	sum := sha256.Sum256([]byte(source))
	if want := hex.EncodeToString(sum[:]); p.SourceSHA256 != want {
		t.Errorf("sha256 = %s, want %s", p.SourceSHA256, want)
	}
	if !p.IsRootVersion {
		t.Error("is_root_version = false, want true")
	}
	if p.Status != PluginStatusActive {
		t.Errorf("status = %s, want active", p.Status)
	}
	if len(repo.audits) != 1 || repo.audits[0].EventType != AuditEventRegistered {
		t.Fatalf("audits = %+v, want one registered entry", repo.audits)
	}
	if repo.audits[0].PluginID != p.ID {
		t.Errorf("audit plugin_id = %s, want %s", repo.audits[0].PluginID, p.ID)
	}
}

// 2: Register malformed manifest JSON.
func TestServiceRegisterMalformedManifest(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	source := `/**
 * @canopy-manifest
 * {"name": "broken", "version":
 * @end-canopy-manifest
 */
function main() {}`

	_, err := svc.Register(context.Background(), testManifest(), source, testAuthorID)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("err = %v, want ErrInvalidManifest", err)
	}
	if len(repo.byID) != 0 {
		t.Errorf("expected no rows, got %d", len(repo.byID))
	}
	if len(repo.audits) != 0 {
		t.Errorf("expected no audit entries, got %d", len(repo.audits))
	}
}

// 3: Register missing entry_point.
func TestServiceRegisterMissingEntryPoint(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	m := testManifest()
	m.EntryPoint = ""
	source := buildSource(t, m, 0)

	_, err := svc.Register(context.Background(), m, source, testAuthorID)
	if !errors.Is(err, ErrManifestValidationFailed) {
		t.Fatalf("err = %v, want ErrManifestValidationFailed", err)
	}
}

// 4: Register bad semver ("1.2").
func TestServiceRegisterBadSemver(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	m := testManifest()
	m.Version = "1.2"
	source := buildSource(t, m, 0)

	_, err := svc.Register(context.Background(), m, source, testAuthorID)
	if !errors.Is(err, ErrManifestValidationFailed) {
		t.Fatalf("err = %v, want ErrManifestValidationFailed", err)
	}
}

// 5: Register unknown permission ("quantum_compute").
func TestServiceRegisterUnknownPermission(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	m := testManifest()
	m.Permissions = []Permission{"quantum_compute"}
	source := buildSource(t, m, 0)

	_, err := svc.Register(context.Background(), m, source, testAuthorID)
	if !errors.Is(err, ErrInvalidPermission) {
		t.Fatalf("err = %v, want ErrInvalidPermission", err)
	}
	if len(repo.byID) != 0 {
		t.Errorf("expected no rows, got %d", len(repo.byID))
	}
}

// 6: Register 2MB source.
func TestServiceRegisterTooLarge(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	source := buildSource(t, testManifest(), 2*1024*1024)

	_, err := svc.Register(context.Background(), testManifest(), source, testAuthorID)
	if !errors.Is(err, ErrPluginTooLarge) {
		t.Fatalf("err = %v, want ErrPluginTooLarge", err)
	}
	if len(repo.byID) != 0 {
		t.Errorf("expected no rows, got %d", len(repo.byID))
	}
}

// 7: Register duplicate (name, version) — idempotent.
func TestServiceRegisterDuplicate(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	source := buildSource(t, testManifest(), 1024)

	first, err := svc.Register(context.Background(), testManifest(), source, testAuthorID)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	second, err := svc.Register(context.Background(), testManifest(), source, testAuthorID)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second.ID = %s, want %s (same row)", second.ID, first.ID)
	}
	if len(repo.order) != 1 {
		t.Errorf("expected 1 row, got %d", len(repo.order))
	}
	if len(repo.audits) != 1 {
		t.Errorf("expected 1 audit entry (no new entry on duplicate), got %d", len(repo.audits))
	}
}

// 8: Register same name, new version — old archived, new linked.
func TestServiceRegisterVersionUpdate(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)

	v1 := testManifest()
	source1 := buildSource(t, v1, 1024)
	old, err := svc.Register(context.Background(), v1, source1, testAuthorID)
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}

	v2 := testManifest()
	v2.Version = "1.1.0"
	source2 := buildSource(t, v2, 1024)
	updated, err := svc.Register(context.Background(), v2, source2, testAuthorID)
	if err != nil {
		t.Fatalf("register v2: %v", err)
	}

	if updated.Status != PluginStatusActive {
		t.Errorf("new version status = %s, want active", updated.Status)
	}
	if updated.PreviousVersionID == nil || *updated.PreviousVersionID != old.ID {
		t.Errorf("previous_version_id = %v, want %s", updated.PreviousVersionID, old.ID)
	}
	if updated.IsRootVersion {
		t.Error("is_root_version = true on update, want false")
	}
	if !old.IsRootVersion {
		t.Error("is_root_version = false on first version, want true")
	}

	storedOld, err := repo.GetByID(context.Background(), old.ID)
	if err != nil {
		t.Fatalf("get old: %v", err)
	}
	if storedOld.Status != PluginStatusArchived {
		t.Errorf("old status = %s, want archived", storedOld.Status)
	}
	if storedOld.ArchivedAt == nil {
		t.Error("old archived_at = nil, want set")
	}
	active, err := repo.GetActiveByName(context.Background(), "csv-viewer")
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active.ID != updated.ID {
		t.Errorf("active = %s, want %s", active.ID, updated.ID)
	}
	if len(repo.audits) != 2 {
		t.Errorf("expected 2 audit entries, got %d", len(repo.audits))
	}
}

// 9: Install plugin to tree.
func TestServiceInstallToTree(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	p := mustRegister(t, svc, testManifest())
	treeID := uuid.New()

	inst, err := svc.Install(context.Background(), p.ID, &treeID, testAuthorID, []Permission{PermissionDataRead})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if inst.Status != InstanceStatusActive {
		t.Errorf("status = %s, want active", inst.Status)
	}
	if inst.TreeID == nil || *inst.TreeID != treeID {
		t.Errorf("tree_id = %v, want %s", inst.TreeID, treeID)
	}
	if len(inst.GrantedPermissions) != 1 || inst.GrantedPermissions[0] != PermissionDataRead {
		t.Errorf("granted = %v, want [data_read]", inst.GrantedPermissions)
	}
	if p.InstallCount != 1 {
		t.Errorf("install_count = %d, want 1", p.InstallCount)
	}
	// installed audit entry exists.
	found := false
	for _, a := range repo.audits {
		if a.EventType == AuditEventInstalled && a.InstanceID != nil && *a.InstanceID == inst.ID {
			found = true
		}
	}
	if !found {
		t.Error("no installed audit entry for the new instance")
	}
}

// 10: Install twice same (plugin, tree, profile).
func TestServiceInstallTwice(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	p := mustRegister(t, svc, testManifest())
	treeID := uuid.New()

	if _, err := svc.Install(context.Background(), p.ID, &treeID, testAuthorID, []Permission{PermissionDataRead}); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	_, err := svc.Install(context.Background(), p.ID, &treeID, testAuthorID, []Permission{PermissionDataRead})
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("second Install err = %v, want ErrAlreadyInstalled", err)
	}
}

// 11: Install with permission not declared.
func TestServiceInstallPermissionNotDeclared(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	p := mustRegister(t, svc, testManifest()) // declares only data_read

	_, err := svc.Install(context.Background(), p.ID, nil, testAuthorID, []Permission{PermissionDataWrite})
	if !errors.Is(err, ErrPermissionNotDeclared) {
		t.Fatalf("err = %v, want ErrPermissionNotDeclared", err)
	}
	if len(repo.instances) != 0 {
		t.Errorf("expected no instances, got %d", len(repo.instances))
	}
}

// 12: Install disabled plugin.
func TestServiceInstallDisabled(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	p := mustRegister(t, svc, testManifest())
	repo.byID[p.ID].Status = PluginStatusDisabled

	_, err := svc.Install(context.Background(), p.ID, nil, testAuthorID, nil)
	if !errors.Is(err, ErrPluginDisabled) {
		t.Fatalf("err = %v, want ErrPluginDisabled", err)
	}
}

// 13: Pause then resume instance.
func TestServicePauseResume(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	p := mustRegister(t, svc, testManifest())
	inst, err := svc.Install(context.Background(), p.ID, nil, testAuthorID, []Permission{PermissionDataRead})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := svc.PauseInstance(context.Background(), inst.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused, err := repo.GetInstance(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("get paused: %v", err)
	}
	if paused.Status != InstanceStatusPaused {
		t.Errorf("status = %s, want paused", paused.Status)
	}

	if err := svc.ResumeInstance(context.Background(), inst.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	resumed, err := repo.GetInstance(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("get resumed: %v", err)
	}
	if resumed.Status != InstanceStatusActive {
		t.Errorf("status = %s, want active", resumed.Status)
	}
	// paused + resumed audit entries.
	events := map[string]bool{}
	for _, a := range repo.audits {
		if a.InstanceID != nil && *a.InstanceID == inst.ID {
			events[a.EventType] = true
		}
	}
	if !events[AuditEventPaused] || !events[AuditEventResumed] {
		t.Errorf("audit events = %v, want paused+resumed", events)
	}
}

// 14: CheckPermission allowed method.
func TestServiceCheckPermissionAllowed(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	p := mustRegister(t, svc, testManifest())
	inst, err := svc.Install(context.Background(), p.ID, nil, testAuthorID, []Permission{PermissionDataRead})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := svc.CheckPermission(context.Background(), inst.ID, "data.query"); err != nil {
		t.Fatalf("CheckPermission(data.query) = %v, want nil", err)
	}
	got, _ := repo.GetInstance(context.Background(), inst.ID)
	if got.InvokeCount != 1 {
		t.Errorf("invoke_count = %d, want 1", got.InvokeCount)
	}
}

// 15: CheckPermission denied method → typed PERMISSION_DENIED.
func TestServiceCheckPermissionDenied(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	p := mustRegister(t, svc, testManifest())
	inst, err := svc.Install(context.Background(), p.ID, nil, testAuthorID, []Permission{PermissionDataRead})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	err = svc.CheckPermission(context.Background(), inst.ID, "data.mutate")
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	var typed *PermissionDeniedError
	if !errors.As(err, &typed) {
		t.Fatalf("err = %T, want *PermissionDeniedError", err)
	}
	if typed.Method != "data.mutate" || typed.Required != PermissionDataWrite {
		t.Errorf("typed = %+v, want method=data.mutate required=data_write", typed)
	}
}

// 16: CheckPermission unknown method.
func TestServiceCheckPermissionUnknownMethod(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	p := mustRegister(t, svc, testManifest())
	inst, err := svc.Install(context.Background(), p.ID, nil, testAuthorID, []Permission{PermissionDataRead})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := svc.CheckPermission(context.Background(), inst.ID, "foo.bar"); !errors.Is(err, ErrAPINotFound) {
		t.Fatalf("err = %v, want ErrAPINotFound", err)
	}
}

// 17: CheckPermission on paused instance.
func TestServiceCheckPermissionPaused(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	p := mustRegister(t, svc, testManifest())
	inst, err := svc.Install(context.Background(), p.ID, nil, testAuthorID, []Permission{PermissionDataRead})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := svc.PauseInstance(context.Background(), inst.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if err := svc.CheckPermission(context.Background(), inst.ID, "data.query"); !errors.Is(err, ErrInstanceNotActive) {
		t.Fatalf("err = %v, want ErrInstanceNotActive", err)
	}
}

// 18: ParseManifest happy path.
func TestParseManifestHappyPath(t *testing.T) {
	src := buildSource(t, testManifest(), 0)
	m, err := ParseManifest(src)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Name != "csv-viewer" || m.Version != "1.0.0" {
		t.Errorf("name/version = %s/%s", m.Name, m.Version)
	}
	if m.RenderType != RenderTypeCard || m.EntryPoint != "main" {
		t.Errorf("render_type/entry_point = %s/%s", m.RenderType, m.EntryPoint)
	}
	if len(m.Permissions) != 1 || m.Permissions[0] != PermissionDataRead {
		t.Errorf("permissions = %v", m.Permissions)
	}
	if m.Description != "View CSV cards" {
		t.Errorf("description = %q", m.Description)
	}
}

// 19: ParseManifest no block.
func TestParseManifestNoBlock(t *testing.T) {
	if _, err := ParseManifest("function main() {}"); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("err = %v, want ErrInvalidManifest", err)
	}
}

// 20: MethodToPermission table.
func TestMethodToPermission(t *testing.T) {
	cases := map[string]Permission{
		"data.query":      PermissionDataRead,
		"data.mutate":     PermissionDataWrite,
		"notify":          PermissionNotification,
		"calendar.query":  PermissionCalendarRead,
		"calendar.create": PermissionCalendarWrite,
		"network.fetch":   PermissionNetworkRequest,
		"foo":             "",
	}
	for method, want := range cases {
		if got := MethodToPermission(method); got != want {
			t.Errorf("MethodToPermission(%q) = %q, want %q", method, got, want)
		}
	}
}

// 21: BuildSrcDoc contains CSP + shim + source + nonce.
func TestBuildSrcDoc(t *testing.T) {
	p := &Plugin{
		ID:           uuid.New(),
		Name:         "csv-viewer",
		SourceJS:     `window.canopy.data.query({collection: "nodes"});`,
		ManifestJSON: []byte(`{"name":"csv-viewer","version":"1.0.0","render_type":"card","entry_point":"main"}`),
	}
	instanceID := uuid.New()
	doc := BuildSrcDoc(p, instanceID, "deadbeef1234", "http://localhost:8080")

	for _, want := range []string{
		"<!doctype html>",
		`default-src 'none'`,
		`connect-src none`,
		`script-src 'unsafe-inline'`,
		`<div id="root"></div>`,
		"window.canopy = {",
		"window.parent.postMessage",
		"deadbeef1234", // nonce substituted
		instanceID.String(),
		p.ID.String(),
		`window.canopy.data.query({collection: "nodes"});`, // source present
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("doc missing %q", want)
		}
	}
	if strings.Contains(doc, "__PLUGIN_ID__") {
		t.Error("doc contains unsubstituted __PLUGIN_ID__ placeholder")
	}
	if strings.Contains(doc, "__INSTANCE_ID__") {
		t.Error("doc contains unsubstituted __INSTANCE_ID__ placeholder")
	}
}

// 22: BuildSrcDoc nonce uniqueness.
func TestBuildSrcDocNonceUniqueness(t *testing.T) {
	p := &Plugin{
		ID:           uuid.New(),
		Name:         "csv-viewer",
		SourceJS:     "function main() {}",
		ManifestJSON: []byte(`{"name":"csv-viewer","version":"1.0.0","render_type":"card","entry_point":"main"}`),
	}
	instanceID := uuid.New()
	docA := BuildSrcDoc(p, instanceID, "nonce-aaaa", "http://localhost:8080")
	docB := BuildSrcDoc(p, instanceID, "nonce-bbbb", "http://localhost:8080")
	if docA == docB {
		t.Error("docs with different nonces must differ")
	}
	if !strings.Contains(docA, "nonce-aaaa") || !strings.Contains(docB, "nonce-bbbb") {
		t.Error("nonce not substituted into doc")
	}
}

// 23: List pagination.
func TestServiceListPagination(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	for i := 1; i <= 3; i++ {
		m := testManifest()
		m.Name = fmt.Sprintf("plugin-%d", i)
		m.Version = "1.0.0"
		source := buildSource(t, m, 0)
		if _, err := svc.Register(context.Background(), m, source, testAuthorID); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}

	page1, total, err := svc.List(context.Background(), 2, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("page1 len = %d, want 2", len(page1))
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	// Newest first: plugin-3, plugin-2.
	if page1[0].Name != "plugin-3" || page1[1].Name != "plugin-2" {
		t.Errorf("page1 order = [%s, %s], want [plugin-3, plugin-2]", page1[0].Name, page1[1].Name)
	}

	page2, total, err := svc.List(context.Background(), 2, 2)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 1 || page2[0].Name != "plugin-1" {
		t.Errorf("page2 = %+v, want [plugin-1]", page2)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
}

// 24: GetSource matches registered source + sha.
func TestServiceGetSource(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	source := buildSource(t, testManifest(), 2048)
	p, err := svc.Register(context.Background(), testManifest(), source, testAuthorID)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	gotSource, gotSHA, err := svc.GetSource(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if gotSource != source {
		t.Error("source mismatch")
	}
	sum := sha256.Sum256([]byte(source))
	if want := hex.EncodeToString(sum[:]); gotSHA != want {
		t.Errorf("sha = %s, want %s", gotSHA, want)
	}
}

// --- Version lifecycle tests (SPEC-PL-01 §4.4 / §12.1 scenarios 8/9/17/18/24) ---

// Update happy path: old active archived + chain-linked, new row active,
// 'updated' audit entry.
func TestServiceUpdateHappyPath(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	v1 := testManifest()
	oldP := mustRegister(t, svc, v1)

	v2 := testManifest()
	v2.Version = "1.1.0"
	source2 := buildSource(t, v2, 1024)
	updated, err := svc.Update(context.Background(), "csv-viewer", source2, testAuthorID)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Version != "1.1.0" || updated.Status != PluginStatusActive {
		t.Errorf("updated = %s@%s, want 1.1.0 active", updated.Name, updated.Version)
	}
	if updated.IsRootVersion {
		t.Error("is_root_version = true on Update, want false")
	}
	if updated.PreviousVersionID == nil || *updated.PreviousVersionID != oldP.ID {
		t.Errorf("previous_version_id = %v, want %s", updated.PreviousVersionID, oldP.ID)
	}

	// Old row: archived, archived_at set, superseded_by_id → new.
	storedOld, err := repo.GetByID(context.Background(), oldP.ID)
	if err != nil {
		t.Fatalf("get old: %v", err)
	}
	if storedOld.Status != PluginStatusArchived || storedOld.ArchivedAt == nil {
		t.Errorf("old status/archived_at = %s/%v, want archived/set", storedOld.Status, storedOld.ArchivedAt)
	}
	if storedOld.SupersededByID == nil || *storedOld.SupersededByID != updated.ID {
		t.Errorf("old superseded_by_id = %v, want %s", storedOld.SupersededByID, updated.ID)
	}

	// Active pointer moved.
	active, err := repo.GetActiveByName(context.Background(), "csv-viewer")
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active.ID != updated.ID {
		t.Errorf("active = %s, want %s", active.ID, updated.ID)
	}

	// 'updated' audit entry with previous_version metadata.
	found := false
	for _, a := range repo.audits {
		if a.EventType == AuditEventUpdated && a.PluginID == updated.ID {
			found = true
			if a.Metadata["previous_version"] != "1.0.0" {
				t.Errorf("metadata = %v, want previous_version=1.0.0", a.Metadata)
			}
		}
	}
	if !found {
		t.Error("no 'updated' audit entry for the new version")
	}
}

// Update same (name, version) → ErrVersionConflict (scenario 9).
func TestServiceUpdateSameVersionConflict(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	mustRegister(t, svc, testManifest())

	source := buildSource(t, testManifest(), 1024)
	_, err := svc.Update(context.Background(), "csv-viewer", source, testAuthorID)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	if len(repo.order) != 1 {
		t.Errorf("rows = %d, want 1 (no new row on conflict)", len(repo.order))
	}
}

// Update unknown name → ErrPluginNotFound.
func TestServiceUpdateUnknownName(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	m := testManifest()
	m.Name = "nope"
	source := buildSource(t, m, 1024)

	_, err := svc.Update(context.Background(), "nope", source, testAuthorID)
	if !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("err = %v, want ErrPluginNotFound", err)
	}
}

// Update with a manifest whose name mismatches the target → validation failure.
func TestServiceUpdateManifestNameMismatch(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	mustRegister(t, svc, testManifest())

	m := testManifest()
	m.Name = "other-plugin"
	m.Version = "1.1.0"
	source := buildSource(t, m, 1024)
	_, err := svc.Update(context.Background(), "csv-viewer", source, testAuthorID)
	if !errors.Is(err, ErrManifestValidationFailed) {
		t.Fatalf("err = %v, want ErrManifestValidationFailed", err)
	}
	if len(repo.order) != 1 {
		t.Errorf("rows = %d, want 1 (no row on validation failure)", len(repo.order))
	}
}

// Update oversized source → ErrPluginTooLarge.
func TestServiceUpdateTooLarge(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1024)
	m := testManifest()
	m.Name = "csv-viewer"
	m.Version = "1.1.0"
	source := buildSource(t, m, 2048)

	// No active row yet — the size gate must fire before any lookup.
	_, err := svc.Update(context.Background(), "csv-viewer", source, testAuthorID)
	if !errors.Is(err, ErrPluginTooLarge) {
		t.Fatalf("err = %v, want ErrPluginTooLarge", err)
	}
	if len(repo.order) != 0 {
		t.Errorf("rows = %d, want 0", len(repo.order))
	}
}

// Rollback happy path: target active, old active archived, links both
// directions, 'rolled_back' audit (scenario 17).
func TestServiceRollbackHappyPath(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	v1 := testManifest()
	v1P, err := svc.Register(context.Background(), v1, buildSource(t, v1, 1024), testAuthorID)
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}
	v2 := testManifest()
	v2.Version = "1.1.0"
	v2P, err := svc.Register(context.Background(), v2, buildSource(t, v2, 1024), testAuthorID)
	if err != nil {
		t.Fatalf("register v2: %v", err)
	}

	rolled, err := svc.Rollback(context.Background(), "csv-viewer", "1.0.0", testAuthorID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.ID != v1P.ID || rolled.Status != PluginStatusActive {
		t.Errorf("rolled = %s@%s (%s), want %s active", rolled.Name, rolled.Version, rolled.Status, v1P.ID)
	}
	if rolled.ArchivedAt != nil {
		t.Error("rolled archived_at = set, want nil")
	}
	// Target's superseded_by_id records the row it replaced (v2).
	if rolled.SupersededByID == nil || *rolled.SupersededByID != v2P.ID {
		t.Errorf("rolled superseded_by_id = %v, want %s", rolled.SupersededByID, v2P.ID)
	}
	// The previously active v2 is archived and points at v1.
	storedV2, err := repo.GetByID(context.Background(), v2P.ID)
	if err != nil {
		t.Fatalf("get v2: %v", err)
	}
	if storedV2.Status != PluginStatusArchived || storedV2.ArchivedAt == nil {
		t.Errorf("v2 status/archived_at = %s/%v, want archived/set", storedV2.Status, storedV2.ArchivedAt)
	}
	if storedV2.SupersededByID == nil || *storedV2.SupersededByID != v1P.ID {
		t.Errorf("v2 superseded_by_id = %v, want %s", storedV2.SupersededByID, v1P.ID)
	}
	// v1's previous_version_id links back to the row it replaced.
	storedV1, err := repo.GetByID(context.Background(), v1P.ID)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if storedV1.PreviousVersionID == nil || *storedV1.PreviousVersionID != v2P.ID {
		t.Errorf("v1 previous_version_id = %v, want %s", storedV1.PreviousVersionID, v2P.ID)
	}

	// Active pointer moved back.
	active, err := repo.GetActiveByName(context.Background(), "csv-viewer")
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active.ID != v1P.ID {
		t.Errorf("active = %s, want %s", active.ID, v1P.ID)
	}

	// 'rolled_back' audit entry.
	found := false
	for _, e := range repo.audits {
		if e.EventType == AuditEventRolledBack && e.PluginID == v1P.ID {
			found = true
			if e.Metadata["previous_version"] != "1.1.0" {
				t.Errorf("metadata = %v, want previous_version=1.1.0", e.Metadata)
			}
		}
	}
	if !found {
		t.Error("no 'rolled_back' audit entry")
	}
}

// Rollback to a version that does not exist → ErrRollbackFailed (scenario 18),
// which unwraps to ErrPluginVersionNotFound.
func TestServiceRollbackUnknownVersion(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	mustRegister(t, svc, testManifest())

	_, err := svc.Rollback(context.Background(), "csv-viewer", "9.9.9", testAuthorID)
	if !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("err = %v, want ErrRollbackFailed", err)
	}
	if !errors.Is(err, ErrPluginVersionNotFound) {
		t.Errorf("err = %v, want unwrap to ErrPluginVersionNotFound", err)
	}
}

// Rollback to the already-active version → ErrRollbackFailed.
func TestServiceRollbackAlreadyActive(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	mustRegister(t, svc, testManifest())

	_, err := svc.Rollback(context.Background(), "csv-viewer", "1.0.0", testAuthorID)
	if !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("err = %v, want ErrRollbackFailed", err)
	}
}

// Rollback unknown plugin name → ErrPluginNotFound.
func TestServiceRollbackUnknownName(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)

	_, err := svc.Rollback(context.Background(), "nope", "1.0.0", testAuthorID)
	if !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("err = %v, want ErrPluginNotFound", err)
	}
}

// ListVersions returns newest-first history (scenario 24).
func TestServiceListVersionsOrdering(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	mustRegister(t, svc, testManifest())
	v2 := testManifest()
	v2.Version = "1.1.0"
	mustRegister(t, svc, v2)

	versions, err := svc.ListVersions(context.Background(), "csv-viewer")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("len = %d, want 2", len(versions))
	}
	if versions[0].Version != "1.1.0" || versions[1].Version != "1.0.0" {
		t.Errorf("order = [%s, %s], want [1.1.0, 1.0.0]", versions[0].Version, versions[1].Version)
	}
	// Slim view: no source leakage.
	if versions[0].Permissions == nil || len(versions[0].Permissions) != 1 {
		t.Errorf("slim permissions = %v, want [data_read]", versions[0].Permissions)
	}
}

// GetVersion found + not-found.
func TestServiceGetVersion(t *testing.T) {
	repo := newStubRepo()
	svc := NewService(repo, 1048576)
	mustRegister(t, svc, testManifest())

	v, err := svc.GetVersion(context.Background(), "csv-viewer", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if v.Version != "1.0.0" || v.Status != PluginStatusActive || v.ID == uuid.Nil {
		t.Errorf("version = %+v, want active 1.0.0", v)
	}

	_, err = svc.GetVersion(context.Background(), "csv-viewer", "9.9.9")
	if !errors.Is(err, ErrPluginVersionNotFound) {
		t.Fatalf("err = %v, want ErrPluginVersionNotFound", err)
	}
}

// --- helpers --------------------------------------------------------------

func mustRegister(t *testing.T, svc Service, m PluginManifest) *Plugin {
	t.Helper()
	p, err := svc.Register(context.Background(), m, buildSource(t, m, 1024), testAuthorID)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return p
}
