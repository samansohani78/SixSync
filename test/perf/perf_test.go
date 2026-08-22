//go:build perf

// Package perf measures SixSync at the scale it is built for.
//
// The target is a gateway with thousands of routes — the case where reviewing
// configuration by hand stopped being possible, which is why the tool exists.
// These run behind the `perf` build tag and use synthetic data only.
package perf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/lintrules"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/plan"
	"github.com/samansohani78/SixSync/internal/policy"
	"github.com/samansohani78/SixSync/internal/resource"
)

// scale is the resource count these measurements target.
const scale = 5000

// buildRepo writes a synthetic repository of the given size.
//
// The shape mirrors a real gateway: many routes over a smaller number of
// shared upstreams and services, which is what makes the dependency graph and
// blast-radius analysis do real work.
func buildRepo(tb testing.TB, routes int) string {
	tb.Helper()
	root := tb.TempDir()

	upstreams := routes / 50
	if upstreams < 1 {
		upstreams = 1
	}
	services := routes / 100
	if services < 1 {
		services = 1
	}

	write := func(dir, name, content string) {
		p := filepath.Join(root, dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			tb.Fatal(err)
		}
	}

	for i := 0; i < upstreams; i++ {
		write("upstreams", fmt.Sprintf("u%05d.yaml", i), fmt.Sprintf(
			"apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: u%05d\n"+
				"lifecycle:\n  state: present\nspec:\n  type: roundrobin\n  scheme: http\n"+
				"  nodes:\n    10.%d.%d.1:8080: 5\n    10.%d.%d.2:8080: 5\n",
			i, i/256, i%256, i/256, i%256))
	}
	for i := 0; i < services; i++ {
		write("services", fmt.Sprintf("s%05d.yaml", i), fmt.Sprintf(
			"apiVersion: sixsync/v1alpha1\nkind: Service\nmetadata:\n  id: s%05d\n"+
				"lifecycle:\n  state: present\nspec:\n  name: service-%d\n  upstream_id: u%05d\n",
			i, i, i%upstreams))
	}
	for i := 0; i < routes; i++ {
		write("routes", fmt.Sprintf("r%05d.yaml", i), fmt.Sprintf(
			"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r%05d\n"+
				"  name: route-%d\nlifecycle:\n  state: present\nspec:\n  name: route-%d\n"+
				"  uri: /api/v1/service%d/resource%d\n  methods:\n  - GET\n  - POST\n"+
				"  service_id: s%05d\n  plugins:\n    proxy-rewrite:\n      uri: /internal/%d\n"+
				"    limit-count:\n      count: 100\n      time_window: 60\n",
			i, i, i, i%services, i, i%services, i))
	}
	return root
}

// gateway serves the same synthetic state as observed remote state.
type gateway struct {
	resources map[model.Kind][]map[string]any
}

func (g *gateway) BaseURL() string { return "http://gateway.example.test:9180" }

func (g *gateway) List(_ context.Context, d model.Definition) ([]apisix.Resource, error) {
	out := make([]apisix.Resource, 0, len(g.resources[d.Kind]))
	for i, v := range g.resources[d.Kind] {
		id, _ := v["id"].(string)
		out = append(out, apisix.Resource{
			Value: v, EtcdKey: "/apisix/" + d.APIPath + "/" + id,
			ModifiedIndex: int64(1000 + i),
		})
	}
	return out, nil
}

// gatewayFrom derives observed state from a repository, so plan sees a
// realistic mix rather than everything being a create.
func gatewayFrom(tb testing.TB, repo *resource.Repo, changeEvery int) *gateway {
	tb.Helper()
	g := &gateway{resources: map[model.Kind][]map[string]any{}}
	for i, l := range repo.Resources {
		spec := map[string]any{}
		for k, v := range l.File.Spec {
			spec[k] = v
		}
		spec["id"] = l.File.Metadata.ID
		spec["create_time"] = 1787430328
		spec["update_time"] = 1787430328
		if changeEvery > 0 && i%changeEvery == 0 {
			spec["uri"] = "/stale-value"
		}
		g.resources[l.File.Kind] = append(g.resources[l.File.Kind], spec)
	}
	return g
}

func measure(tb testing.TB, name string, fn func()) time.Duration {
	tb.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	start := time.Now()
	fn()
	elapsed := time.Since(start)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	tb.Logf("%-28s %8s  %6d MiB allocated  peak heap %d MiB",
		name, elapsed.Round(time.Millisecond), allocated>>20, after.HeapAlloc>>20)
	return elapsed
}

func TestScaleLoad(t *testing.T) {
	root := buildRepo(t, scale)

	var repo *resource.Repo
	elapsed := measure(t, "load 5000 resources", func() {
		var err error
		repo, err = resource.Load(root)
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(repo.Resources) < scale {
		t.Fatalf("loaded %d resources, want at least %d", len(repo.Resources), scale)
	}
	// A generous ceiling: the point is to catch an accidental quadratic, not
	// to police milliseconds on unknown hardware.
	if elapsed > 60*time.Second {
		t.Errorf("loading %d resources took %s, which suggests a scaling problem",
			scale, elapsed)
	}
}

func TestScaleLint(t *testing.T) {
	repo, err := resource.Load(buildRepo(t, scale))
	if err != nil {
		t.Fatal(err)
	}

	var findings []lintrules.Finding
	elapsed := measure(t, "lint 5000 resources", func() {
		findings = lintrules.Check(repo)
	})
	t.Logf("findings: %d", len(findings))

	if elapsed > 60*time.Second {
		t.Errorf("linting took %s", elapsed)
	}
}

func TestScaleCanonicalize(t *testing.T) {
	repo, err := resource.Load(buildRepo(t, scale))
	if err != nil {
		t.Fatal(err)
	}

	elapsed := measure(t, "canonicalize + digest", func() {
		for _, l := range repo.Resources {
			c, err := canonical.Canonicalize(l.File.Kind, l.File.Spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := canonical.Digest(c); err != nil {
				t.Fatal(err)
			}
		}
	})
	if elapsed > 60*time.Second {
		t.Errorf("canonicalization took %s", elapsed)
	}
}

func TestScalePlan(t *testing.T) {
	repo, err := resource.Load(buildRepo(t, scale))
	if err != nil {
		t.Fatal(err)
	}
	g := gatewayFrom(t, repo, 10) // one in ten differs

	var p *plan.Plan
	elapsed := measure(t, "plan 5000 resources", func() {
		p, err = plan.Build(context.Background(), plan.Options{
			Repo: repo, Client: g, Profile: policy.DefaultProfile(),
			Target: plan.Target{AdminURL: g.BaseURL()},
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	n := p.Content.Summarize()
	t.Logf("operations: %d planned updates, %d unchanged", n.Update, n.Unchanged)
	if n.Update == 0 {
		t.Error("the fixture should produce changes")
	}
	if elapsed > 120*time.Second {
		t.Errorf("planning took %s", elapsed)
	}
}

func TestScalePlanSerialization(t *testing.T) {
	repo, err := resource.Load(buildRepo(t, scale))
	if err != nil {
		t.Fatal(err)
	}
	g := gatewayFrom(t, repo, 10)

	p, err := plan.Build(context.Background(), plan.Options{
		Repo: repo, Client: g, Profile: policy.DefaultProfile(),
		Target: plan.Target{AdminURL: g.BaseURL()},
	})
	if err != nil {
		t.Fatal(err)
	}

	var size int
	elapsed := measure(t, "serialize plan", func() {
		f, err := os.CreateTemp(t.TempDir(), "plan-*.json")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := plan.Write(f, p); err != nil {
			t.Fatal(err)
		}
		info, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		size = int(info.Size())
	})
	t.Logf("plan artifact: %d KiB", size>>10)
	if elapsed > 60*time.Second {
		t.Errorf("serializing took %s", elapsed)
	}
}

// Determinism must hold at scale, not just on small fixtures: map iteration
// disorder is far more likely to surface with thousands of entries.
func TestScaleDeterminism(t *testing.T) {
	root := buildRepo(t, 1000)

	var first string
	for run := 0; run < 3; run++ {
		repo, err := resource.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		g := gatewayFrom(t, repo, 10)
		p, err := plan.Build(context.Background(), plan.Options{
			Repo: repo, Client: g, Profile: policy.DefaultProfile(),
			Target: plan.Target{AdminURL: g.BaseURL()},
		})
		if err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			first = p.Digest
			continue
		}
		if p.Digest != first {
			t.Fatalf("plan digest varied at scale: %s then %s", first, p.Digest)
		}
	}
	t.Logf("digest stable across 3 runs at 1000 resources: %s", first)
}

func BenchmarkCanonicalizeRoute(b *testing.B) {
	repo, err := resource.Load(buildRepo(b, 100))
	if err != nil {
		b.Fatal(err)
	}
	spec := repo.Resources[0].File.Spec
	kind := repo.Resources[0].File.Kind

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := canonical.Canonicalize(kind, spec); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPlan1000(b *testing.B) {
	repo, err := resource.Load(buildRepo(b, 1000))
	if err != nil {
		b.Fatal(err)
	}
	g := gatewayFrom(b, repo, 10)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := plan.Build(context.Background(), plan.Options{
			Repo: repo, Client: g, Profile: policy.DefaultProfile(),
			Target: plan.Target{AdminURL: g.BaseURL()},
		}); err != nil {
			b.Fatal(err)
		}
	}
}
