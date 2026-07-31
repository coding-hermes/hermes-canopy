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

// --- helpers --------------------------------------------------------------

func mustRegister(t *testing.T, svc Service, m PluginManifest) *Plugin {
	t.Helper()
	p, err := svc.Register(context.Background(), m, buildSource(t, m, 1024), testAuthorID)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return p
}
