package apply

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/model"
)

// fakeGateway is an in-memory APISIX with programmable failures.
//
// It models the behaviours that matter for apply: revisions advance on every
// write, reads reflect writes, and any operation can be made to fail in a
// specific way at a specific point.
type fakeGateway struct {
	mu    sync.Mutex
	store map[string]map[string]any
	// index tracks each resource's revision, advancing on every write exactly
	// as APISIX does.
	index map[string]int64
	next  int64

	// failOn maps "METHOD Kind/id" to the error to return.
	failOn map[string]error
	// mutateOnWrite rewrites what gets stored, to model a gateway that accepts
	// a write but stores something else.
	mutateOnWrite func(kind model.Kind, id string, spec map[string]any) map[string]any

	// calls records every request, for asserting what did and did not happen.
	calls []string
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{
		store:  map[string]map[string]any{},
		index:  map[string]int64{},
		next:   100,
		failOn: map[string]error{},
	}
}

func (f *fakeGateway) BaseURL() string { return "http://gateway.example.test:9180" }

func gwKey(kind model.Kind, id string) string { return string(kind) + "/" + id }

func (f *fakeGateway) seed(kind model.Kind, id string, spec map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.store[gwKey(kind, id)] = clone(spec)
	f.index[gwKey(kind, id)] = f.next
}

func (f *fakeGateway) revision(kind model.Kind, id string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.index[gwKey(kind, id)]
}

func (f *fakeGateway) has(kind model.Kind, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.store[gwKey(kind, id)]
	return ok
}

func (f *fakeGateway) record(method string, kind model.Kind, id string) {
	f.calls = append(f.calls, method+" "+gwKey(kind, id))
}

func (f *fakeGateway) countCalls(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeGateway) Get(_ context.Context, d model.Definition, id string) (apisix.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("GET", d.Kind, id)

	if err := f.failOn["GET "+gwKey(d.Kind, id)]; err != nil {
		return apisix.Resource{}, err
	}
	v, ok := f.store[gwKey(d.Kind, id)]
	if !ok {
		return apisix.Resource{}, &apisix.Error{
			Kind: apisix.ErrNotFound, StatusCode: 404, Method: "GET",
			Path: "/" + d.APIPath + "/" + id, Message: "Key not found",
		}
	}
	return apisix.Resource{
		Value:         clone(v),
		EtcdKey:       "/apisix/" + d.APIPath + "/" + id,
		ModifiedIndex: f.index[gwKey(d.Kind, id)],
	}, nil
}

func (f *fakeGateway) List(_ context.Context, d model.Definition) ([]apisix.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []apisix.Resource
	for k, v := range f.store {
		if !strings.HasPrefix(k, string(d.Kind)+"/") {
			continue
		}
		id := strings.TrimPrefix(k, string(d.Kind)+"/")
		out = append(out, apisix.Resource{
			Value: clone(v), EtcdKey: "/apisix/" + d.APIPath + "/" + id,
			ModifiedIndex: f.index[k],
		})
	}
	return out, nil
}

func (f *fakeGateway) Put(
	_ context.Context, d model.Definition, id string, spec map[string]any,
) (apisix.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("PUT", d.Kind, id)

	if err := f.failOn["PUT "+gwKey(d.Kind, id)]; err != nil {
		return apisix.Resource{}, err
	}

	stored := clone(spec)
	if f.mutateOnWrite != nil {
		stored = f.mutateOnWrite(d.Kind, id, stored)
	}
	// APISIX sets these itself and rejects them from a client.
	stored["id"] = id
	stored["update_time"] = json.Number("1787430328")

	f.next++
	f.store[gwKey(d.Kind, id)] = stored
	f.index[gwKey(d.Kind, id)] = f.next

	return apisix.Resource{Value: clone(stored)}, nil
}

func (f *fakeGateway) Delete(_ context.Context, d model.Definition, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("DELETE", d.Kind, id)

	if err := f.failOn["DELETE "+gwKey(d.Kind, id)]; err != nil {
		return err
	}
	if _, ok := f.store[gwKey(d.Kind, id)]; !ok {
		return &apisix.Error{
			Kind: apisix.ErrNotFound, StatusCode: 404, Method: "DELETE",
			Path: "/" + d.APIPath + "/" + id, Message: "Key not found",
		}
	}
	delete(f.store, gwKey(d.Kind, id))
	delete(f.index, gwKey(d.Kind, id))
	return nil
}

// clone deep-copies through JSON so stored values behave like decoded ones.
func clone(v map[string]any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		panic(err)
	}
	return out
}

func apiErr(kind apisix.ErrorKind, status int, msg string) error {
	return &apisix.Error{Kind: kind, StatusCode: status, Method: "PUT", Path: "/x", Message: msg}
}

func transportErr() error {
	return &apisix.Error{Kind: apisix.ErrTransport, Method: "PUT", Path: "/x"}
}
