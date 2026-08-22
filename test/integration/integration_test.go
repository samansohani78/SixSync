//go:build integration

// Package integration exercises SixSync against a real APISIX instance.
//
// These tests are behind the `integration` build tag and never run as part of
// `go test ./...`. They require the disposable stack in deploy/compose, which
// is pinned to an exact APISIX version so that a passing run names the build it
// passed against.
//
// They must only ever be pointed at a throwaway instance. Every test here
// writes resources, and the seeded values are synthetic.
package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/model"
)

// adminURL and the keys come from the environment so that the disposable stack
// is never hard-coded to a real endpoint.
func clientFor(t *testing.T, key string) *apisix.Client {
	t.Helper()

	url := os.Getenv("SIXSYNC_TEST_ADMIN_URL")
	if url == "" {
		t.Skip("SIXSYNC_TEST_ADMIN_URL is not set; start deploy/compose first")
	}

	c, err := apisix.New(apisix.Options{
		BaseURL: url, APIKey: key, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	return c
}

func adminClient(t *testing.T) *apisix.Client {
	return clientFor(t, os.Getenv("SIXSYNC_TEST_ADMIN_KEY"))
}

func viewerClient(t *testing.T) *apisix.Client {
	key := os.Getenv("SIXSYNC_TEST_VIEWER_KEY")
	if key == "" {
		t.Skip("SIXSYNC_TEST_VIEWER_KEY is not set")
	}
	return clientFor(t, key)
}

// The response envelope is the thing most likely to change between APISIX
// releases, and the one SixSync depends on most.
func TestLiveEnvelopeShape(t *testing.T) {
	c := adminClient(t)
	def, _ := model.Lookup(model.KindUpstream)

	got, err := c.List(context.Background(), def)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) == 0 {
		t.Skip("no upstreams present; seed the instance first")
	}

	r := got[0]
	if r.EtcdKey == "" {
		t.Error("the envelope carried no key")
	}
	// modifiedIndex is SixSync's concurrency token. If a future APISIX stops
	// returning it, that design has to change, and this test is the alarm.
	if r.ModifiedIndex == 0 {
		t.Error("modifiedIndex is absent; SixSync's conflict detection depends on it")
	}
	if len(r.Value) == 0 {
		t.Error("the envelope carried no value")
	}
}

// doctor's capability probe must distinguish a viewer key from an
// administrator key, and must not mutate anything doing so.
func TestLiveCapabilityDetection(t *testing.T) {
	ctx := context.Background()

	admin := adminClient(t).Diagnose(ctx)
	if admin.Capability != apisix.CapabilityAdmin {
		t.Errorf("administrator key detected as %s", admin.Capability)
	}
	if admin.APISIXVersion == "" {
		t.Error("the APISIX version was not discovered from the Server header")
	}

	viewer := viewerClient(t).Diagnose(ctx)
	if viewer.Capability != apisix.CapabilityReadOnly {
		t.Errorf("viewer key detected as %s", viewer.Capability)
	}
	if !viewer.Authenticated {
		t.Error("a viewer key can read, so it must count as authenticated")
	}
}

// The finding this whole design rests on: since 3.16 APISIX does not write
// schema defaults back, so SixSync needs no per-version default table. If a
// future release reverts that, diffs would fill with phantom changes and this
// test is where it surfaces.
func TestLiveDefaultsAreNotPopulated(t *testing.T) {
	c := adminClient(t)
	def, _ := model.Lookup(model.KindRoute)

	routes, err := c.List(context.Background(), def)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	for _, r := range routes {
		// Only meaningful for a route that never set these explicitly, which
		// the seeded minimal route does not.
		id, _ := r.ID(def)
		if id != "e2e-minimal" {
			continue
		}
		for _, field := range []string{"priority", "status"} {
			if _, present := r.Value[field]; present {
				t.Errorf("APISIX populated %q on a route that did not set it; "+
					"canonicalization assumptions need revisiting", field)
			}
		}
		return
	}
	t.Skip("route e2e-minimal not present; seed the instance first")
}

// A resource read twice, unchanged, must canonicalize to the same digest.
func TestLiveDigestIsStable(t *testing.T) {
	c := adminClient(t)
	def, _ := model.Lookup(model.KindUpstream)
	ctx := context.Background()

	first, err := c.List(ctx, def)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first) == 0 {
		t.Skip("no upstreams present")
	}
	second, err := c.List(ctx, def)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	for i := range first {
		a, err := canonical.Canonicalize(def.Kind, first[i].Value)
		if err != nil {
			t.Fatalf("canonicalizing: %v", err)
		}
		b, err := canonical.Canonicalize(def.Kind, second[i].Value)
		if err != nil {
			t.Fatalf("canonicalizing: %v", err)
		}
		da, _ := canonical.Digest(a)
		db, _ := canonical.Digest(b)
		if da != db {
			t.Errorf("%s digest changed between two reads with no write in between",
				first[i].EtcdKey)
		}
	}
}

// APISIX strips SSL private keys on read. SixSync refuses to import SSL
// objects anyway, but if that ever changed silently this is the alarm.
func TestLiveSSLPrivateKeyIsNeverReturned(t *testing.T) {
	c := adminClient(t)
	def, _ := model.Lookup(model.KindSSL)

	ssls, err := c.List(context.Background(), def)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, s := range ssls {
		for _, field := range []string{"key", "keys"} {
			if _, present := s.Value[field]; present {
				t.Errorf("%s: APISIX returned %q on read; it is expected to strip it",
					s.EtcdKey, field)
			}
		}
	}
}
