package apisix

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAPISIX mimics the responses observed from apache/apisix:3.18.0-debian
// for the key/role combinations `doctor` has to tell apart.
type fakeAPISIX struct {
	adminKey  string
	viewerKey string
	// noSchemaEndpoint simulates a build without schema validation.
	noSchemaEndpoint bool
	// schemaValidateNotRoleGated models APISIX 3.16.0, where the schema
	// validation endpoint does not enforce the viewer role.
	schemaValidateNotRoleGated bool
	// omitServerHeader simulates a proxy that strips Server.
	omitServerHeader bool

	mutations int // must remain zero: doctor may never write
}

func (f *fakeAPISIX) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !f.omitServerHeader {
		w.Header().Set("Server", "APISIX/3.18.0")
	}
	w.Header().Set("X-API-VERSION", "v3")
	w.Header().Set("Content-Type", "application/json")

	key := r.Header.Get("X-API-KEY")
	isAdmin := key != "" && key == f.adminKey
	isViewer := key != "" && key == f.viewerKey

	// Anything that could change stored state would be a bug in doctor.
	//
	// The capability probe is a DELETE against an identifier APISIX can never
	// store, so it is exempt: it cannot remove anything that exists.
	// r.URL.Path is already percent-decoded, so match the identifier itself.
	if r.Method != http.MethodGet &&
		!strings.HasPrefix(r.URL.Path, "/apisix/admin/schema/validate/") &&
		!strings.Contains(r.URL.Path, CapabilityProbeID) {
		f.mutations++
	}

	switch {
	case key == "":
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error_msg":"failed to check token","description":"missing apikey"}`)
		return
	case !isAdmin && !isViewer:
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error_msg":"failed to check token","description":"wrong apikey"}`)
		return
	}

	// The capability probe: a DELETE against an identifier that cannot exist.
	if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, CapabilityProbeID) {
		if isViewer {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error_msg":"failed to check token","description":"invalid method for role viewer"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Key not found"}`)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/apisix/admin/schema/validate/") {
		if f.noSchemaEndpoint {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"not found"}`)
			return
		}
		if isViewer && !f.schemaValidateNotRoleGated {
			// APISIX checks the role before dispatching to the validator.
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error_msg":"failed to check token","description":"invalid method for role viewer"}`)
			return
		}
		// A bare {"uri":"/"} genuinely fails route validation on 3.18.0
		// because a route also needs an upstream or plugins.
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error_msg":"object matches none of the required: [\"plugins\",\"uri\"] or [\"upstream\",\"uri\"]"}`)
		return
	}

	fmt.Fprint(w, `{"list":[],"total":0}`)
}

func diagnoseWith(t *testing.T, f *fakeAPISIX, key string) Diagnosis {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	c, err := New(Options{BaseURL: srv.URL, APIKey: key, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c.Diagnose(context.Background())
}

func TestDiagnoseIdentifiesAnAdministratorKey(t *testing.T) {
	f := &fakeAPISIX{adminKey: "admin-k", viewerKey: "viewer-k"}
	d := diagnoseWith(t, f, "admin-k")

	if !d.Reachable || !d.Authenticated {
		t.Fatalf("expected a reachable, authenticated endpoint: %+v", d)
	}
	if d.Capability != CapabilityAdmin {
		t.Errorf("Capability = %s, want %s", d.Capability, CapabilityAdmin)
	}
	if d.APISIXVersion != "3.18.0" {
		t.Errorf("APISIXVersion = %q, want 3.18.0", d.APISIXVersion)
	}
	if d.AdminAPIVersion != "v3" {
		t.Errorf("AdminAPIVersion = %q, want v3", d.AdminAPIVersion)
	}
	if !d.SchemaValidationAvailable {
		t.Error("schema validation should be reported as available")
	}
	if len(d.Problems) != 0 {
		t.Errorf("unexpected problems: %v", d.Problems)
	}
}

// The capability probe is the reason doctor can report least privilege at all.
func TestDiagnoseIdentifiesAViewerKey(t *testing.T) {
	f := &fakeAPISIX{adminKey: "admin-k", viewerKey: "viewer-k"}
	d := diagnoseWith(t, f, "viewer-k")

	if !d.Authenticated {
		t.Fatalf("a viewer key can read, so it must count as authenticated: %+v", d)
	}
	if d.Capability != CapabilityReadOnly {
		t.Errorf("Capability = %s, want %s", d.Capability, CapabilityReadOnly)
	}
}

// The single most important property of doctor.
func TestDiagnoseNeverMutates(t *testing.T) {
	for _, key := range []string{"admin-k", "viewer-k", "wrong-k", ""} {
		f := &fakeAPISIX{adminKey: "admin-k", viewerKey: "viewer-k"}
		_ = diagnoseWith(t, f, key)
		if f.mutations != 0 {
			t.Errorf("doctor issued %d state-changing request(s) with key %q", f.mutations, key)
		}
	}
}

func TestDiagnoseRejectedKeys(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantCap KeyCapability
	}{
		{"unknown key", "not-a-real-key", CapabilityNone},
		{"no key at all", "", CapabilityNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAPISIX{adminKey: "admin-k", viewerKey: "viewer-k"}
			d := diagnoseWith(t, f, tt.key)

			if !d.Reachable {
				t.Error("endpoint answered, so it must be reported as reachable")
			}
			if d.Authenticated {
				t.Error("a rejected key must not be reported as authenticated")
			}
			if d.Capability != tt.wantCap {
				t.Errorf("Capability = %s, want %s", d.Capability, tt.wantCap)
			}
			if len(d.Problems) == 0 {
				t.Error("a rejected key must produce a problem the user can act on")
			}
		})
	}
}

func TestDiagnoseReportsMissingSchemaEndpoint(t *testing.T) {
	f := &fakeAPISIX{adminKey: "admin-k", noSchemaEndpoint: true}
	d := diagnoseWith(t, f, "admin-k")

	if d.SchemaValidationAvailable {
		t.Error("schema validation must not be reported as available when it 404s")
	}
	if len(d.Problems) == 0 {
		t.Fatal("a missing schema endpoint should be surfaced as a problem")
	}
	if !strings.Contains(strings.Join(d.Problems, " "), "validate --remote") {
		t.Errorf("the problem should name the affected command: %v", d.Problems)
	}
}

// A proxy may strip Server; that is a gap in information, not a failure.
func TestDiagnoseToleratesMissingServerHeader(t *testing.T) {
	f := &fakeAPISIX{adminKey: "admin-k", omitServerHeader: true}
	d := diagnoseWith(t, f, "admin-k")

	if !d.Authenticated {
		t.Error("a stripped Server header must not affect authentication")
	}
	if d.APISIXVersion != "" {
		t.Errorf("APISIXVersion = %q, want empty when undeterminable", d.APISIXVersion)
	}
}

func TestDiagnoseReportsUnreachableEndpoint(t *testing.T) {
	c, err := New(Options{BaseURL: "http://127.0.0.1:1", APIKey: "k", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := c.Diagnose(context.Background())

	if d.Reachable {
		t.Error("a refused connection must not be reported as reachable")
	}
	if len(d.Problems) == 0 {
		t.Error("an unreachable endpoint must produce a problem")
	}
	if d.Capability != CapabilityUnknown {
		t.Errorf("Capability = %s, want %s when the endpoint is unreachable", d.Capability, CapabilityUnknown)
	}
}

// Diagnosis is printed and may be serialized; it must never carry the key.
func TestDiagnosisContainsNoCredential(t *testing.T) {
	const secret = "super-secret-admin-key"
	f := &fakeAPISIX{adminKey: secret}
	d := diagnoseWith(t, f, secret)

	rendered := fmt.Sprintf("%+v", d)
	if strings.Contains(rendered, secret) {
		t.Errorf("the API key leaked into the diagnosis: %s", rendered)
	}
}

// APISIX 3.16.0 does not enforce the viewer role on the schema validation
// endpoint; 3.17.0 began requiring the admin key there (#13328). The capability
// probe must not depend on that endpoint, or every viewer key on 3.16 is
// reported as an administrator — which is exactly the mistake an operator
// would act on when trying to run least-privilege.
func TestDiagnoseIdentifiesViewerOnAPISIX316(t *testing.T) {
	f := &fakeAPISIX{
		adminKey: "admin-k", viewerKey: "viewer-k",
		schemaValidateNotRoleGated: true, // 3.16.0 behaviour
	}
	d := diagnoseWith(t, f, "viewer-k")

	if d.Capability != CapabilityReadOnly {
		t.Errorf("Capability = %s, want %s; the probe must not rely on schema validation",
			d.Capability, CapabilityReadOnly)
	}
	if f.mutations != 0 {
		t.Errorf("doctor issued %d state-changing request(s)", f.mutations)
	}
}

// The probe identifier must be one APISIX can never store, so the DELETE
// cannot remove a real resource.
func TestCapabilityProbeIdentifierCannotExist(t *testing.T) {
	// APISIX's id schema is ^[a-zA-Z0-9-_.]+$ with length 1..64.
	for _, r := range CapabilityProbeID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			// An accepted character on its own is fine.
		default:
			return // at least one character APISIX rejects: the identifier is unstorable
		}
	}
	t.Fatalf("CapabilityProbeID %q contains only characters APISIX accepts, so a resource "+
		"could exist with that identifier and the probe could delete it", CapabilityProbeID)
}

// The probe is a DELETE. If it ever targeted a storable identifier, that would
// be a destructive bug; assert the request shape explicitly.
func TestCapabilityProbeTargetsOnlyTheUnstorableIdentifier(t *testing.T) {
	var deletePaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			deletePaths = append(deletePaths, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Key not found"}`)
			return
		}
		fmt.Fprint(w, `{"list":[],"total":0}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Options{BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Diagnose(context.Background())

	if len(deletePaths) != 1 {
		t.Fatalf("expected exactly one DELETE, got %d: %v", len(deletePaths), deletePaths)
	}
	if !strings.Contains(deletePaths[0], CapabilityProbeID) {
		t.Errorf("DELETE targeted %q, which is not the probe identifier", deletePaths[0])
	}
}
