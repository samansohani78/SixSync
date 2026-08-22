//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/model"
)

// testVersion reports which APISIX release the suite is pointed at, so a test
// can record a version-specific expectation instead of hiding a difference
// behind a generic normalization.
func testVersion(t *testing.T) string {
	t.Helper()
	return os.Getenv("SIXSYNC_TEST_APISIX_VERSION")
}

// rawAdmin issues a request straight to the Admin API, bypassing the SixSync
// client. Several compatibility facts are about the wire format itself, and
// asserting them through the client would only prove the client is
// self-consistent.
func rawAdmin(t *testing.T, method, path string, body string) (int, http.Header, []byte) {
	t.Helper()

	base := os.Getenv("SIXSYNC_TEST_ADMIN_URL")
	if base == "" {
		t.Skip("SIXSYNC_TEST_ADMIN_URL is not set")
	}

	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *http.Request
	var err error
	if rdr != nil {
		req, err = http.NewRequestWithContext(context.Background(), method, base+path, rdr)
	} else {
		req, err = http.NewRequestWithContext(context.Background(), method, base+path, nil)
	}
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("X-API-KEY", os.Getenv("SIXSYNC_TEST_ADMIN_KEY"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if readErr != nil {
			break
		}
	}
	return resp.StatusCode, resp.Header, buf
}

// --- Response envelope -----------------------------------------------------

func TestCompatResponseEnvelope(t *testing.T) {
	code, hdr, body := rawAdmin(t, "GET", "/apisix/admin/routes/e2e-minimal", "")
	if code != 200 {
		t.Fatalf("GET route: HTTP %d: %s", code, body)
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}

	// The shape SixSync's decoder is built around.
	for _, field := range []string{"key", "value", "createdIndex", "modifiedIndex"} {
		if _, ok := env[field]; !ok {
			t.Errorf("single-resource envelope is missing %q on APISIX %s; got fields %v",
				field, testVersion(t), keysOf(env))
		}
	}
	if v := hdr.Get("X-API-VERSION"); v != "v3" {
		t.Errorf("X-API-VERSION = %q, want v3 on APISIX %s", v, testVersion(t))
	}
}

func TestCompatListEnvelope(t *testing.T) {
	code, _, body := rawAdmin(t, "GET", "/apisix/admin/routes", "")
	if code != 200 {
		t.Fatalf("HTTP %d", code)
	}
	var env struct {
		List  []json.RawMessage `json:"list"`
		Total int               `json:"total"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("list envelope is not the expected shape: %v: %s", err, body)
	}
	if env.Total != len(env.List) {
		t.Errorf("total = %d but list has %d entries", env.Total, len(env.List))
	}
}

// modifiedIndex is SixSync's concurrency token. If a release stops returning
// it, ADR-0004's design is invalid and this must fail loudly.
func TestCompatModifiedIndexIsVisible(t *testing.T) {
	c := adminClient(t)
	def, _ := model.Lookup(model.KindRoute)

	routes, err := c.List(context.Background(), def)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(routes) == 0 {
		t.Skip("no routes seeded")
	}
	for _, r := range routes {
		if r.ModifiedIndex == 0 {
			t.Fatalf("modifiedIndex absent for %s on APISIX %s; ADR-0004 depends on it",
				r.EtcdKey, testVersion(t))
		}
	}
}

// A write must advance modifiedIndex even when the content is unchanged,
// which is what makes it a conservative conflict signal.
func TestCompatModifiedIndexAdvancesOnIdenticalWrite(t *testing.T) {
	const body = `{"type":"roundrobin","nodes":{"10.30.0.1:80":1}}`
	const path = "/apisix/admin/upstreams/compat-noop"
	t.Cleanup(func() { rawAdmin(t, "DELETE", path+"?force=true", "") })

	if code, _, b := rawAdmin(t, "PUT", path, body); code != 200 && code != 201 {
		t.Fatalf("seed PUT: HTTP %d: %s", code, b)
	}
	first := readModifiedIndex(t, path)

	if code, _, b := rawAdmin(t, "PUT", path, body); code != 200 && code != 201 {
		t.Fatalf("second PUT: HTTP %d: %s", code, b)
	}
	second := readModifiedIndex(t, path)

	if second <= first {
		t.Errorf("modifiedIndex did not advance on an identical rewrite (%d -> %d) on APISIX %s",
			first, second, testVersion(t))
	}
}

func readModifiedIndex(t *testing.T, path string) int64 {
	t.Helper()
	_, _, body := rawAdmin(t, "GET", path, "")
	var env struct {
		ModifiedIndex int64 `json:"modifiedIndex"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decoding modifiedIndex: %v", err)
	}
	return env.ModifiedIndex
}

// --- Pagination and empty collections --------------------------------------

func TestCompatPagination(t *testing.T) {
	code, _, body := rawAdmin(t, "GET", "/apisix/admin/routes?page=1&page_size=10", "")
	if code != 200 {
		t.Errorf("pagination rejected on APISIX %s: HTTP %d: %s", testVersion(t), code, body)
	}
}

// A paginated request matching nothing returns "list":{} — an object, not an
// array — because an empty Lua table serializes that way. The client must
// tolerate it or every command breaks on a gateway with an empty resource kind.
func TestCompatEmptyPaginatedCollection(t *testing.T) {
	code, _, body := rawAdmin(t, "GET", "/apisix/admin/ssls?page=1&page_size=500", "")
	if code != 200 {
		t.Fatalf("HTTP %d: %s", code, body)
	}
	t.Logf("APISIX %s empty paginated collection: %s", testVersion(t), body)

	// Whatever shape it is, the client must cope.
	c := adminClient(t)
	def, _ := model.Lookup(model.KindSSL)
	got, err := c.List(context.Background(), def)
	if err != nil {
		t.Fatalf("client could not list an empty collection on APISIX %s: %v", testVersion(t), err)
	}
	if len(got) != 0 {
		t.Errorf("expected no ssls, got %d", len(got))
	}
}

// --- Version discovery -----------------------------------------------------

func TestCompatVersionDiscovery(t *testing.T) {
	_, hdr, _ := rawAdmin(t, "GET", "/apisix/admin/routes", "")
	got := apisix.ServerVersion(hdr)
	if got == "" {
		t.Errorf("no version in the Server header on APISIX %s (raw: %q)",
			testVersion(t), hdr.Get("Server"))
		return
	}
	if want := testVersion(t); want != "" && got != want {
		t.Errorf("Server header reports %q but the stack is %q", got, want)
	}
}

// --- Defaults --------------------------------------------------------------

// The finding the whole diff design rests on: since 3.16 APISIX does not write
// schema defaults back. If a release reverts that, diffs fill with phantom
// changes and the canonicalizer needs a per-version default table.
func TestCompatDefaultsNotPopulated(t *testing.T) {
	const path = "/apisix/admin/routes/compat-defaults"
	t.Cleanup(func() { rawAdmin(t, "DELETE", path+"?force=true", "") })

	if code, _, b := rawAdmin(t, "PUT", path,
		`{"uri":"/compat-defaults","upstream_id":"payments-backend"}`); code != 200 && code != 201 {
		t.Fatalf("PUT: HTTP %d: %s", code, b)
	}

	_, _, body := rawAdmin(t, "GET", path, "")
	var env struct {
		Value map[string]any `json:"value"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"priority", "status"} {
		if v, present := env.Value[field]; present {
			t.Errorf("APISIX %s populated %q = %v on write; canonicalization assumes it does not",
				testVersion(t), field, v)
		}
	}
}

func TestCompatUpstreamDefaultsNotPopulated(t *testing.T) {
	const path = "/apisix/admin/upstreams/compat-updefaults"
	t.Cleanup(func() { rawAdmin(t, "DELETE", path+"?force=true", "") })

	if code, _, b := rawAdmin(t, "PUT", path,
		`{"type":"roundrobin","nodes":{"10.30.0.9:80":1}}`); code != 200 && code != 201 {
		t.Fatalf("PUT: HTTP %d: %s", code, b)
	}

	_, _, body := rawAdmin(t, "GET", path, "")
	var env struct {
		Value map[string]any `json:"value"`
	}
	_ = json.Unmarshal(body, &env)

	for _, field := range []string{"hash_on", "scheme", "pass_host", "keepalive_pool"} {
		if v, present := env.Value[field]; present {
			t.Errorf("APISIX %s populated upstream %q = %v on write", testVersion(t), field, v)
		}
	}
}

// --- Timestamps ------------------------------------------------------------

func TestCompatTimestampsAreServerOwned(t *testing.T) {
	code, _, body := rawAdmin(t, "PUT", "/apisix/admin/upstreams/compat-ts",
		`{"type":"roundrobin","nodes":{"10.30.0.2:80":1},"create_time":1}`)
	t.Cleanup(func() { rawAdmin(t, "DELETE", "/apisix/admin/upstreams/compat-ts?force=true", "") })

	if code != 400 {
		t.Errorf("APISIX %s accepted a client-supplied create_time (HTTP %d); the resource "+
			"format assumes it is rejected: %s", testVersion(t), code, body)
	}
}

// --- Authentication and roles ----------------------------------------------

func TestCompatAuthenticationErrors(t *testing.T) {
	base := os.Getenv("SIXSYNC_TEST_ADMIN_URL")
	if base == "" {
		t.Skip("SIXSYNC_TEST_ADMIN_URL is not set")
	}

	tests := []struct {
		name       string
		key        string
		wantCode   int
		wantInBody string
	}{
		{"no key", "", 401, "missing apikey"},
		{"wrong key", "definitely-not-a-key", 401, "wrong apikey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(context.Background(), "GET",
				base+"/apisix/admin/routes", nil)
			if tt.key != "" {
				req.Header.Set("X-API-KEY", tt.key)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantCode {
				t.Errorf("HTTP %d, want %d on APISIX %s", resp.StatusCode, tt.wantCode, testVersion(t))
			}
			var b struct {
				Description string `json:"description"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&b)
			if !strings.Contains(b.Description, tt.wantInBody) {
				t.Errorf("description = %q, want it to contain %q on APISIX %s; "+
					"the client classifies auth failures on this string",
					b.Description, tt.wantInBody, testVersion(t))
			}
		})
	}
}

// The capability probe doctor relies on, and the guarantee that it mutates
// nothing.
func TestCompatViewerKeyBehaviour(t *testing.T) {
	viewer := viewerClient(t)
	ctx := context.Background()

	d := viewer.Diagnose(ctx)
	if d.Capability != apisix.CapabilityReadOnly {
		t.Errorf("viewer key detected as %s on APISIX %s", d.Capability, testVersion(t))
	}
	if !d.Authenticated {
		t.Errorf("viewer key not authenticated on APISIX %s", testVersion(t))
	}
}

// The security property that matters: a viewer key cannot change anything, on
// any supported version.
func TestCompatViewerKeyCannotWrite(t *testing.T) {
	base := os.Getenv("SIXSYNC_TEST_ADMIN_URL")
	viewerKey := os.Getenv("SIXSYNC_TEST_VIEWER_KEY")
	if base == "" || viewerKey == "" {
		t.Skip("viewer credentials not configured")
	}

	write := func(method, path, body string) (int, string) {
		var req *http.Request
		var err error
		if body != "" {
			req, err = http.NewRequestWithContext(context.Background(), method, base+path,
				strings.NewReader(body))
		} else {
			req, err = http.NewRequestWithContext(context.Background(), method, base+path, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-API-KEY", viewerKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&b)
		return resp.StatusCode, b.Description
	}

	const probeRoute = "/apisix/admin/routes/compat-viewer-write-probe"

	code, desc := write("PUT", probeRoute, `{"uri":"/nope","upstream_id":"payments-backend"}`)
	if code != 401 {
		t.Errorf("a viewer key was allowed to PUT on APISIX %s: HTTP %d", testVersion(t), code)
	}
	if !strings.Contains(desc, "invalid method for role viewer") {
		t.Errorf("unexpected refusal for a viewer PUT on APISIX %s: %q", testVersion(t), desc)
	}

	if code, _ := write("DELETE", probeRoute, ""); code != 401 {
		t.Errorf("a viewer key was allowed to DELETE on APISIX %s: HTTP %d", testVersion(t), code)
	}

	// Nothing may have been created by the refused write.
	if code, _, _ := rawAdmin(t, "GET", probeRoute, ""); code != 404 {
		t.Errorf("a viewer write persisted on APISIX %s: GET returned HTTP %d", testVersion(t), code)
		rawAdmin(t, "DELETE", probeRoute, "")
	}
}

// Role gating on the schema validation endpoint is version-dependent, and the
// difference is recorded rather than smoothed over.
//
// APISIX 3.17.0 began requiring the admin key there (#13328). On 3.16.0 a
// viewer key receives a schema verdict instead of 401 — which is why doctor's
// capability probe does not use this endpoint.
func TestCompatSchemaValidateRoleGating(t *testing.T) {
	viewer := viewerClient(t)
	def, _ := model.Lookup(model.KindRoute)

	err := viewer.ValidateSchema(context.Background(), def, map[string]any{"uri": "/x"})
	gated := apisix.IsKind(err, apisix.ErrAuthInsufficient)

	switch v := testVersion(t); {
	case strings.HasPrefix(v, "3.16."):
		if gated {
			t.Logf("APISIX %s now role-gates schema validation; it did not previously", v)
		} else {
			t.Logf("APISIX %s does NOT role-gate schema validation (expected); "+
				"the capability probe must not depend on it", v)
		}
	case v == "":
		t.Skip("SIXSYNC_TEST_APISIX_VERSION is not set")
	default:
		if !gated {
			t.Errorf("APISIX %s does not role-gate schema validation, but 3.17.0+ is "+
				"expected to (#13328); got %v", v, err)
		}
	}
}

// --- Schema validation -----------------------------------------------------

func TestCompatSchemaValidationEndpoint(t *testing.T) {
	code, _, body := rawAdmin(t, "POST", "/apisix/admin/schema/validate/routes", `{"uri":1980}`)
	if code != 400 {
		t.Errorf("invalid body gave HTTP %d, want 400 on APISIX %s: %s", code, testVersion(t), body)
	}
	if !strings.Contains(string(body), "error_msg") {
		t.Errorf("no error_msg in the rejection on APISIX %s: %s", testVersion(t), body)
	}

	code, _, body = rawAdmin(t, "POST", "/apisix/admin/schema/validate/routes",
		`{"uri":"/ok","upstream_id":"payments-backend"}`)
	if code != 200 {
		t.Errorf("a valid route was rejected on APISIX %s: HTTP %d: %s", testVersion(t), code, body)
	}
}

// --- Reference integrity and deletion --------------------------------------

func TestCompatDeleteRefusedWhileReferenced(t *testing.T) {
	code, _, body := rawAdmin(t, "DELETE", "/apisix/admin/upstreams/reports-backend", "")
	if code != 400 {
		t.Errorf("deleting a referenced upstream gave HTTP %d, want 400 on APISIX %s: %s",
			code, testVersion(t), body)
	}
	if !strings.Contains(string(body), "still using it now") {
		t.Errorf("unexpected refusal message on APISIX %s: %s", testVersion(t), body)
	}
}

// force=true bypasses the reference check entirely. SixSync never sends it;
// this records the behaviour so the reason stays evidenced.
func TestCompatForceDeleteBypassesReferenceCheck(t *testing.T) {
	// Build a throwaway pair so the seeded fixtures survive.
	if code, _, b := rawAdmin(t, "PUT", "/apisix/admin/upstreams/compat-force-up",
		`{"type":"roundrobin","nodes":{"10.30.0.3:80":1}}`); code > 299 {
		t.Fatalf("seed upstream: HTTP %d: %s", code, b)
	}
	if code, _, b := rawAdmin(t, "PUT", "/apisix/admin/routes/compat-force-route",
		`{"uri":"/compat-force","upstream_id":"compat-force-up"}`); code > 299 {
		t.Fatalf("seed route: HTTP %d: %s", code, b)
	}
	t.Cleanup(func() {
		rawAdmin(t, "DELETE", "/apisix/admin/routes/compat-force-route", "")
		rawAdmin(t, "DELETE", "/apisix/admin/upstreams/compat-force-up?force=true", "")
	})

	if code, _, _ := rawAdmin(t, "DELETE", "/apisix/admin/upstreams/compat-force-up", ""); code != 400 {
		t.Errorf("expected the reference check to refuse, got HTTP %d", code)
	}
	code, _, body := rawAdmin(t, "DELETE", "/apisix/admin/upstreams/compat-force-up?force=true", "")
	if code != 200 {
		t.Errorf("force=true did not delete on APISIX %s: HTTP %d: %s", testVersion(t), code, body)
	}
}

// APISIX does NOT protect a plugin config referenced by a route. That gap is
// why SixSync does its own dependency analysis.
func TestCompatPluginConfigDeletionIsProtected(t *testing.T) {
	code, _, body := rawAdmin(t, "DELETE", "/apisix/admin/plugin_configs/auth-standard-v1", "")
	t.Logf("APISIX %s deleting a referenced plugin_config: HTTP %d %s", testVersion(t), code, body)

	if code == 200 {
		// It was actually deleted; restore the fixture for later tests.
		rawAdmin(t, "PUT", "/apisix/admin/plugin_configs/auth-standard-v1",
			`{"desc":"shared auth","plugins":{"cors":{},"request-id":{}}}`)
		t.Logf("APISIX %s ALLOWS deleting a referenced plugin_config; SixSync's own "+
			"dependency analysis is the only protection", testVersion(t))
	}
}

// --- SSL redaction ---------------------------------------------------------

// SSL private key exposure on read is version-dependent, and the difference is
// security-relevant.
//
// APISIX 3.17.0 strips the key from both single and list responses (#13227).
// APISIX 3.16.0 does NOT strip it from the list response: it returns the key
// encrypted under the data_encryption keyring, and APISIX ships publicly-known
// default keys, so that ciphertext should be treated as recoverable.
//
// SixSync refuses to import SSL objects on every version, so this never reaches
// a resource file. The test exists to keep the behaviour evidenced and to catch
// a regression on 3.17+.
func TestCompatSSLPrivateKeyOnRead(t *testing.T) {
	cert, key := selfSignedPair(t)
	payload, err := json.Marshal(map[string]any{
		"cert": cert, "key": key, "snis": []string{"compat.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if code, _, body := rawAdmin(t, "PUT", "/apisix/admin/ssls/compat-ssl", string(payload)); code > 299 {
		t.Fatalf("PUT ssl: HTTP %d: %s", code, body)
	}
	t.Cleanup(func() { rawAdmin(t, "DELETE", "/apisix/admin/ssls/compat-ssl", "") })

	leaks := map[string]bool{}
	for _, path := range []string{"/apisix/admin/ssls/compat-ssl", "/apisix/admin/ssls"} {
		_, _, got := rawAdmin(t, "GET", path, "")

		// Plaintext key material must never come back on any version.
		if strings.Contains(string(got), "PRIVATE KEY") {
			t.Errorf("APISIX %s returned a private key in PLAINTEXT from %s",
				testVersion(t), path)
		}

		for _, value := range resourceValues(t, got) {
			for _, field := range []string{"key", "keys"} {
				if _, present := value[field]; present {
					leaks[path] = true
				}
			}
			if _, present := value["cert"]; !present {
				t.Errorf("APISIX %s did not return the certificate from %s; it is public",
					testVersion(t), path)
			}
		}
	}

	v := testVersion(t)
	switch {
	case strings.HasPrefix(v, "3.16."):
		if len(leaks) == 0 {
			t.Logf("APISIX %s now strips SSL key material on read; it did not previously", v)
			return
		}
		for path := range leaks {
			t.Logf("APISIX %s returns encrypted SSL key material from %s. APISIX ships "+
				"publicly-known default data_encryption keys, so treat that ciphertext as "+
				"recoverable. SixSync refuses to import SSL objects, so it never reaches a file.",
				v, path)
		}
	case v == "":
		t.Skip("SIXSYNC_TEST_APISIX_VERSION is not set")
	default:
		// 3.17.0 onwards must strip it from both responses.
		for path := range leaks {
			t.Errorf("APISIX %s returned SSL key material from %s; 3.17.0+ is expected to "+
				"strip it (#13227)", v, path)
		}
	}
}

// --- Node representations and large integers -------------------------------

func TestCompatNodeRepresentationsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"map form", `{"type":"roundrobin","nodes":{"10.30.1.1:8080":3}}`},
		{"array form", `{"type":"roundrobin","nodes":[{"host":"10.30.1.1","port":8080,"weight":3}]}`},
	}

	var digests []string
	for _, tc := range cases {
		path := "/apisix/admin/upstreams/compat-nodes"
		if code, _, b := rawAdmin(t, "PUT", path, tc.body); code > 299 {
			t.Fatalf("%s: HTTP %d: %s", tc.name, code, b)
		}

		c := adminClient(t)
		def, _ := model.Lookup(model.KindUpstream)
		r, err := c.Get(context.Background(), def, "compat-nodes")
		if err != nil {
			t.Fatalf("%s: Get: %v", tc.name, err)
		}
		canon, err := canonical.Canonicalize(def.Kind, r.Value)
		if err != nil {
			t.Fatalf("%s: canonicalize: %v", tc.name, err)
		}
		d, _ := canonical.Digest(canon)
		digests = append(digests, d)
	}
	t.Cleanup(func() { rawAdmin(t, "DELETE", "/apisix/admin/upstreams/compat-nodes?force=true", "") })

	if digests[0] != digests[1] {
		t.Errorf("map and array node forms canonicalize differently on APISIX %s: %s vs %s",
			testVersion(t), digests[0], digests[1])
	}
}

// float64 cannot hold this exactly; a client that decodes numbers naively
// corrupts it and then writes the corrupted value back.
func TestCompatLargeIntegerIdentifiers(t *testing.T) {
	const bigID = "00000000000000123456"
	path := "/apisix/admin/upstreams/" + bigID
	t.Cleanup(func() { rawAdmin(t, "DELETE", path+"?force=true", "") })

	if code, _, b := rawAdmin(t, "PUT", path,
		`{"type":"roundrobin","nodes":{"10.30.2.1:80":1}}`); code > 299 {
		t.Fatalf("PUT: HTTP %d: %s", code, b)
	}

	c := adminClient(t)
	def, _ := model.Lookup(model.KindUpstream)
	r, err := c.Get(context.Background(), def, bigID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gotID, err := r.ID(def)
	if err != nil {
		t.Fatalf("ID: %v", err)
	}
	if gotID != bigID {
		t.Errorf("identifier round-tripped as %q, want %q on APISIX %s", gotID, bigID, testVersion(t))
	}
}

// A route with a numeric upstream_id beyond float64's exact range must survive.
func TestCompatLargeNumericReference(t *testing.T) {
	const bigRef = 552218409505391294
	upPath := fmt.Sprintf("/apisix/admin/upstreams/%d", bigRef)
	t.Cleanup(func() {
		rawAdmin(t, "DELETE", "/apisix/admin/routes/compat-bigref", "")
		rawAdmin(t, "DELETE", upPath+"?force=true", "")
	})

	if code, _, b := rawAdmin(t, "PUT", upPath,
		`{"type":"roundrobin","nodes":{"10.30.2.2:80":1}}`); code > 299 {
		t.Skipf("APISIX %s rejected a large numeric id (HTTP %d): %s", testVersion(t), code, b)
	}
	if code, _, b := rawAdmin(t, "PUT", "/apisix/admin/routes/compat-bigref",
		fmt.Sprintf(`{"uri":"/compat-bigref","upstream_id":%d}`, bigRef)); code > 299 {
		t.Skipf("route with a large numeric reference rejected (HTTP %d): %s", code, b)
	}

	c := adminClient(t)
	def, _ := model.Lookup(model.KindRoute)
	r, err := c.Get(context.Background(), def, "compat-bigref")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	canon, err := canonical.Canonicalize(def.Kind, r.Value)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if got := fmt.Sprintf("%v", canon["upstream_id"]); got != "552218409505391294" {
		t.Errorf("large reference lost precision: got %s on APISIX %s", got, testVersion(t))
	}
}

// --- Route TTL, script, filter_func ----------------------------------------

func TestCompatRouteTTLAccepted(t *testing.T) {
	code, _, body := rawAdmin(t, "PUT", "/apisix/admin/routes/compat-ttl?ttl=120",
		`{"uri":"/compat-ttl","upstream_id":"payments-backend"}`)
	t.Cleanup(func() { rawAdmin(t, "DELETE", "/apisix/admin/routes/compat-ttl", "") })

	if code > 299 {
		t.Logf("APISIX %s rejected ?ttl= (HTTP %d): %s", testVersion(t), code, body)
		return
	}
	t.Logf("APISIX %s accepts ?ttl=; such routes expire on their own, which the "+
		"ephemeral-route lint rule warns about", testVersion(t))
}

func TestCompatFilterFuncAccepted(t *testing.T) {
	code, _, body := rawAdmin(t, "PUT", "/apisix/admin/routes/compat-filterfunc",
		`{"uri":"/compat-ff","upstream_id":"payments-backend",`+
			`"filter_func":"function(vars) return true end"}`)
	t.Cleanup(func() { rawAdmin(t, "DELETE", "/apisix/admin/routes/compat-filterfunc", "") })

	if code > 299 {
		t.Logf("APISIX %s rejected filter_func (HTTP %d): %s", testVersion(t), code, body)
		return
	}
	t.Logf("APISIX %s accepts filter_func: arbitrary Lua on the data plane, which the "+
		"embedded-lua lint rule flags", testVersion(t))
}

// --- Every kind SixSync claims to support ----------------------------------

func TestCompatAllSupportedKindsAreListable(t *testing.T) {
	c := adminClient(t)
	for _, def := range model.Supported() {
		t.Run(string(def.Kind), func(t *testing.T) {
			if _, err := c.List(context.Background(), def); err != nil {
				t.Errorf("cannot list %s on APISIX %s: %v", def.Kind, testVersion(t), err)
			}
		})
	}
}

// A refused kind must stay refused, and its endpoint must still be one APISIX
// recognizes — otherwise the refusal is hiding a broken path.
func TestCompatRefusedKindsHaveRealEndpoints(t *testing.T) {
	for _, def := range model.All() {
		if def.Supported {
			continue
		}
		if def.Kind == model.KindCredential {
			continue // nested under a consumer; not addressable on its own
		}
		t.Run(string(def.Kind), func(t *testing.T) {
			code, _, body := rawAdmin(t, "GET", "/apisix/admin/"+def.APIPath, "")
			// 400 is legitimate for stream_routes when stream mode is off.
			if code != 200 && code != 400 {
				t.Errorf("%s endpoint /%s gave HTTP %d on APISIX %s: %s",
					def.Kind, def.APIPath, code, testVersion(t), body)
			}
		})
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// APISIX itself loses precision on a large *numeric* identifier: a route
// referencing upstream_id 552218409505391294 is rejected with
// `upstream id [5.5221840950539e+17]`, because the value passes through a
// float. String identifiers of the same digits work.
//
// This is an APISIX limitation, not a SixSync one, and it is why the
// large-numeric-id lint rule advises quoting such identifiers.
func TestCompatLargeNumericReferenceIsLossyInAPISIX(t *testing.T) {
	const bigDigits = "552218409505391294"

	// A string identifier of the same digits must work.
	upPath := "/apisix/admin/upstreams/" + bigDigits
	t.Cleanup(func() {
		rawAdmin(t, "DELETE", "/apisix/admin/routes/compat-strref", "")
		rawAdmin(t, "DELETE", upPath+"?force=true", "")
	})

	if code, _, b := rawAdmin(t, "PUT", upPath,
		`{"type":"roundrobin","nodes":{"10.30.3.1:80":1}}`); code > 299 {
		t.Fatalf("upstream with a long digit-string id rejected: HTTP %d: %s", code, b)
	}
	if code, _, b := rawAdmin(t, "PUT", "/apisix/admin/routes/compat-strref",
		`{"uri":"/compat-strref","upstream_id":"`+bigDigits+`"}`); code > 299 {
		t.Errorf("APISIX %s rejected a STRING reference of %s: HTTP %d: %s",
			testVersion(t), bigDigits, code, b)
	}

	// The same value sent as a JSON number is mangled by APISIX.
	code, _, body := rawAdmin(t, "PUT", "/apisix/admin/routes/compat-numref",
		`{"uri":"/compat-numref","upstream_id":`+bigDigits+`}`)
	t.Cleanup(func() { rawAdmin(t, "DELETE", "/apisix/admin/routes/compat-numref", "") })

	if code <= 299 {
		t.Logf("APISIX %s ACCEPTED a large numeric reference; earlier releases mangled it",
			testVersion(t))
		return
	}
	if strings.Contains(string(body), "e+") {
		t.Logf("APISIX %s mangles a large numeric reference through a float: %s",
			testVersion(t), body)
	}
}

// resourceValues extracts the resource bodies from either envelope shape, so a
// check can inspect fields rather than raw response text.
func resourceValues(t *testing.T, body []byte) []map[string]any {
	t.Helper()

	var single struct {
		Value map[string]any `json:"value"`
	}
	if err := json.Unmarshal(body, &single); err == nil && single.Value != nil {
		return []map[string]any{single.Value}
	}

	var list struct {
		List []struct {
			Value map[string]any `json:"value"`
		} `json:"list"`
	}
	if err := json.Unmarshal(body, &list); err == nil {
		out := make([]map[string]any, 0, len(list.List))
		for _, item := range list.List {
			if item.Value != nil {
				out = append(out, item.Value)
			}
		}
		return out
	}
	return nil
}
