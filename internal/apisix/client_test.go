package apisix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samansohani78/SixSync/internal/model"
)

func routeDef(t *testing.T) model.Definition {
	t.Helper()
	d, ok := model.Lookup(model.KindRoute)
	if !ok {
		t.Fatal("Route kind not registered")
	}
	return d
}

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := New(Options{BaseURL: srv.URL, APIKey: "test-key", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain host", "http://gw.example.test:9180", "http://gw.example.test:9180", false},
		{"trailing slash", "http://gw.example.test:9180/", "http://gw.example.test:9180", false},
		{"admin suffix stripped", "http://gw.example.test:9180/apisix/admin", "http://gw.example.test:9180", false},
		{"admin suffix with slash", "http://gw.example.test:9180/apisix/admin/", "http://gw.example.test:9180", false},
		{"query discarded", "https://gw.example.test/?a=b", "https://gw.example.test", false},
		{"https preserved", "https://gw.example.test", "https://gw.example.test", false},
		{"whitespace tolerated", "  http://gw.example.test  ", "http://gw.example.test", false},
		{"empty", "", "", true},
		{"no scheme", "gw.example.test:9180", "", true},
		{"bad scheme", "ftp://gw.example.test", "", true},
		{"no host", "http://", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeBaseURL(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("normalizeBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The API key must travel in the header and nowhere else: a query parameter
// would be recorded in access logs and proxy logs.
func TestAPIKeyIsSentOnlyAsAHeader(t *testing.T) {
	var gotHeader, gotQuery, gotCookie string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-API-KEY")
		gotQuery = r.URL.Query().Get("api_key")
		if ck, err := r.Cookie("x-api-key"); err == nil {
			gotCookie = ck.Value
		}
		writeList(w)
	}))

	if _, err := c.List(context.Background(), routeDef(t)); err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotHeader != "test-key" {
		t.Errorf("X-API-KEY header = %q, want the configured key", gotHeader)
	}
	if gotQuery != "" {
		t.Errorf("api_key leaked into the query string as %q", gotQuery)
	}
	if gotCookie != "" {
		t.Errorf("api key leaked into a cookie as %q", gotCookie)
	}
}

func writeList(w http.ResponseWriter, values ...map[string]any) {
	items := make([]map[string]any, 0, len(values))
	for i, v := range values {
		id, _ := v["id"].(string)
		items = append(items, map[string]any{
			"key":           "/apisix/routes/" + id,
			"value":         v,
			"createdIndex":  100 + i,
			"modifiedIndex": 200 + i,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"list": items, "total": len(items)})
}

// The envelope shape here is the one observed from apache/apisix:3.18.0-debian.
func TestGetDecodesObservedEnvelope(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apisix/admin/routes/r-probe" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"createdIndex":28,
		  "value":{"uri":"/probe","upstream_id":"u-probe","id":"r-probe",
		           "create_time":1787430328,"update_time":1787430328},
		  "key":"/apisix/routes/r-probe","modifiedIndex":29}`)
	}))

	got, err := c.Get(context.Background(), routeDef(t), "r-probe")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.EtcdKey != "/apisix/routes/r-probe" {
		t.Errorf("EtcdKey = %q", got.EtcdKey)
	}
	if got.ModifiedIndex != 29 || got.CreatedIndex != 28 {
		t.Errorf("indices = (created %d, modified %d), want (28, 29)", got.CreatedIndex, got.ModifiedIndex)
	}
	if got.Value["uri"] != "/probe" {
		t.Errorf("value not decoded: %#v", got.Value)
	}
	// Unknown/extra fields must survive: APISIX gains fields between releases.
	if _, ok := got.Value["create_time"]; !ok {
		t.Error("create_time was dropped from the decoded value")
	}
}

func TestGetEscapesTheID(t *testing.T) {
	var gotPath string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"key":"k","value":{"id":"x"},"createdIndex":1,"modifiedIndex":1}`)
	}))
	if _, err := c.Get(context.Background(), routeDef(t), "a/../b"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(gotPath, "/../") {
		t.Errorf("path traversal reached the wire: %q", gotPath)
	}
}

func TestListFollowsPagination(t *testing.T) {
	// Two full pages then a short one, which is how a real list terminates.
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if got := r.URL.Query().Get("page_size"); got != strconv.Itoa(listPageSize) {
			t.Errorf("page_size = %q, want %d", got, listPageSize)
		}

		var items []map[string]any
		switch page {
		case 1, 2:
			for i := 0; i < listPageSize; i++ {
				items = append(items, map[string]any{"id": fmt.Sprintf("p%d-%04d", page, i)})
			}
		default:
			items = []map[string]any{{"id": "last"}}
		}
		writeList(w, items...)
	}))

	got, err := c.List(context.Background(), routeDef(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := listPageSize*2 + 1; len(got) != want {
		t.Errorf("got %d resources across pages, want %d", len(got), want)
	}
}

func TestListDeduplicatesAcrossPages(t *testing.T) {
	// A concurrent write can shift ordering so the same key appears twice.
	calls := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var items []map[string]any
		if calls == 1 {
			for i := 0; i < listPageSize; i++ {
				items = append(items, map[string]any{"id": fmt.Sprintf("r%04d", i)})
			}
		} else {
			// Repeat one already-seen id alongside a new one.
			items = []map[string]any{{"id": "r0000"}, {"id": "brand-new"}}
		}
		writeList(w, items...)
	}))

	got, err := c.List(context.Background(), routeDef(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[string]int{}
	for _, r := range got {
		seen[r.EtcdKey]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("resource %s appeared %d times; duplicates must be collapsed", k, n)
		}
	}
}

// Import and drift must not vary between runs.
func TestListOrderingIsDeterministic(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately unsorted.
		writeList(w,
			map[string]any{"id": "zebra"},
			map[string]any{"id": "alpha"},
			map[string]any{"id": "middle"},
		)
	}))

	var first []string
	for run := 0; run < 10; run++ {
		got, err := c.List(context.Background(), routeDef(t))
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var keys []string
		for _, r := range got {
			keys = append(keys, r.EtcdKey)
		}
		if run == 0 {
			first = keys
			continue
		}
		if strings.Join(keys, ",") != strings.Join(first, ",") {
			t.Fatalf("ordering varied between runs:\n%v\n%v", first, keys)
		}
	}
	if len(first) != 3 || first[0] != "/apisix/routes/alpha" {
		t.Errorf("results are not sorted by etcd key: %v", first)
	}
}

// Every classification below mirrors a response captured from a real 3.18.0
// instance; see docs/compatibility.md.
func TestErrorClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		{
			name: "missing key", status: 401,
			body: `{"error_msg":"failed to check token","description":"missing apikey"}`,
			want: ErrAuthMissing,
		},
		{
			name: "wrong key", status: 401,
			body: `{"error_msg":"failed to check token","description":"wrong apikey"}`,
			want: ErrAuthInvalid,
		},
		{
			name: "viewer key attempting a write", status: 401,
			body: `{"error_msg":"failed to check token","description":"invalid method for role viewer"}`,
			want: ErrAuthInsufficient,
		},
		{
			name: "not found", status: 404,
			body: `{"message":"Key not found"}`,
			want: ErrNotFound,
		},
		{
			name: "schema rejection", status: 400,
			body: `{"error_msg":"property \"uri\" validation failed: wrong type: expected string, got number"}`,
			want: ErrValidation,
		},
		{
			name: "delete refused by reference check", status: 400,
			body: `{"error_msg":"can not delete this upstream, route [r-probe] is still using it now"}`,
			want: ErrReferenced,
		},
		{
			name: "lost compare-and-swap", status: 503,
			body: `{"error_msg":"value changed before overwritten"}`,
			want: ErrConflict,
		},
		{
			name: "genuine outage is not a conflict", status: 503,
			body: `{"error_msg":"etcd unavailable"}`,
			want: ErrServer,
		},
		{
			name: "server error", status: 500,
			body: `{"error_msg":"boom"}`,
			want: ErrServer,
		},
		{
			name: "non-JSON error body is tolerated", status: 502,
			body: `<html>bad gateway</html>`,
			want: ErrServer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			_, err := c.Get(context.Background(), routeDef(t), "x")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !IsKind(err, tt.want) {
				t.Errorf("classified as %v, want %s", err, tt.want)
			}
		})
	}
}

func TestProtocolErrorOnMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not json", `not json at all`},
		{"envelope without value", `{"key":"/apisix/routes/1","modifiedIndex":1}`},
		{"value is not an object", `{"key":"k","value":"a string"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			}))
			_, err := c.Get(context.Background(), routeDef(t), "x")
			if !IsKind(err, ErrProtocol) {
				t.Errorf("got %v, want an ErrProtocol", err)
			}
		})
	}
}

func TestTransportErrorOnUnreachableEndpoint(t *testing.T) {
	c, err := New(Options{BaseURL: "http://127.0.0.1:1", APIKey: "k", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Get(context.Background(), routeDef(t), "x")
	if !IsKind(err, ErrTransport) {
		t.Errorf("got %v, want an ErrTransport", err)
	}
}

func TestRequestHonoursContextCancellation(t *testing.T) {
	release := make(chan struct{})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Get(ctx, routeDef(t), "x")
	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error does not unwrap to context.Canceled: %v", err)
	}
}

func TestRequestHonoursTimeout(t *testing.T) {
	release := make(chan struct{})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Get(ctx, routeDef(t), "x")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout was not honoured; took %s", elapsed)
	}
	if !IsKind(err, ErrTransport) {
		t.Errorf("timeout classified as %v, want ErrTransport", err)
	}
}

// A hostile or broken endpoint must not be able to exhaust client memory.
func TestResponseSizeIsCapped(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Stream far more than the cap.
		chunk := strings.Repeat("a", 1<<20)
		for i := 0; i < 80; i++ {
			if _, err := fmt.Fprint(w, chunk); err != nil {
				return
			}
		}
	}))

	// The point is that this terminates rather than consuming without bound.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Get(context.Background(), routeDef(t), "x")
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("read did not terminate; the response size cap is not effective")
	}
}

func TestServerVersion(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"APISIX/3.18.0", "3.18.0"},
		{"APISIX/3.16.0", "3.16.0"},
		{"nginx", ""},
		{"", ""},
	}
	for _, tt := range tests {
		h := http.Header{}
		if tt.in != "" {
			h.Set("Server", tt.in)
		}
		if got := ServerVersion(h); got != tt.want {
			t.Errorf("ServerVersion(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClientCertAndKeyMustBePaired(t *testing.T) {
	_, err := New(Options{BaseURL: "https://gw.example.test", ClientCertFile: "/tmp/only-cert.pem"})
	if err == nil {
		t.Fatal("expected an error when only a client certificate is supplied")
	}
	if !strings.Contains(err.Error(), "together") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestCAFileMustContainPEM(t *testing.T) {
	f := t.TempDir() + "/ca.pem"
	if err := writeFile(f, "definitely not a certificate"); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{BaseURL: "https://gw.example.test", CAFile: f})
	if err == nil {
		t.Fatal("expected an error for a CA file with no valid PEM")
	}
}

// APISIX serializes an empty Lua table as a JSON object, so a paginated
// request that matches nothing returns {"list":{},"total":0}. Without
// tolerating that, every command fails against a gateway that has no
// resources of some kind — the first-run experience.
func TestListToleratesEmptyListShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty array", `{"list":[],"total":0}`},
		{"empty object, as APISIX sends when paginating an empty collection", `{"list":{},"total":0}`},
		{"null list", `{"list":null,"total":0}`},
		{"list field absent", `{"total":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tt.body)
			}))

			got, err := c.List(context.Background(), routeDef(t))
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("expected no resources, got %d", len(got))
			}
		})
	}
}

// A genuinely malformed list must still be reported rather than silently
// treated as empty.
func TestListRejectsAMalformedListField(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"list":"not a list at all","total":0}`)
	}))
	_, err := c.List(context.Background(), routeDef(t))
	if !IsKind(err, ErrProtocol) {
		t.Errorf("got %v, want an ErrProtocol", err)
	}
}

// The configured timeout must actually bound a request. It was once computed
// and then discarded, which made --timeout silently inert.
func TestConfiguredTimeoutIsApplied(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	c, err := New(Options{BaseURL: srv.URL, APIKey: "k", Timeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	// A background context with no deadline of its own: only the client's
	// configured timeout can end this.
	_, err = c.Get(context.Background(), routeDef(t), "x")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the request to time out")
	}
	if !IsKind(err, ErrTransport) {
		t.Errorf("classified as %v, want ErrTransport", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the configured timeout was ignored; request took %s", elapsed)
	}
}

// A caller's shorter deadline must still win over the client's.
func TestCallerDeadlineTakesPrecedenceWhenShorter(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	c, err := New(Options{BaseURL: srv.URL, APIKey: "k", Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.Get(ctx, routeDef(t), "x"); err == nil {
		t.Fatal("expected a timeout")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the caller's shorter deadline was ignored; took %s", elapsed)
	}
}
