package exitcode

import (
	"errors"
	"fmt"
	"testing"
)

// The numeric values are a public contract. If this test needs changing,
// that is a breaking change to every pipeline that consumes SixSync.
func TestCodeValuesAreStable(t *testing.T) {
	want := map[Code]int{
		OK: 0, Failure: 1, Diff: 2, Conflict: 3,
		PolicyDenied: 4, RollbackIncomplete: 5, Usage: 64,
	}
	for c, n := range want {
		if int(c) != n {
			t.Errorf("exit code %s = %d, want %d", c, int(c), n)
		}
	}
}

func TestCodeString(t *testing.T) {
	tests := []struct {
		code Code
		want string
	}{
		{OK, "ok"},
		{Failure, "failure"},
		{Diff, "diff"},
		{Conflict, "conflict"},
		{PolicyDenied, "policy-denied"},
		{RollbackIncomplete, "rollback-incomplete"},
		{Usage, "usage"},
		{Code(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.code.String(); got != tt.want {
			t.Errorf("Code(%d).String() = %q, want %q", int(tt.code), got, tt.want)
		}
	}
}

func TestFrom(t *testing.T) {
	base := errors.New("boom")

	tests := []struct {
		name string
		err  error
		want Code
	}{
		{"nil error is OK", nil, OK},
		{"plain error is Failure", base, Failure},
		{"wrapped code is preserved", Wrap(Conflict, base), Conflict},
		{"code survives fmt.Errorf wrapping", fmt.Errorf("context: %w", Wrap(Diff, base)), Diff},
		{"Wrap(nil) stays nil", Wrap(Conflict, nil), OK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := From(tt.err); got != tt.want {
				t.Errorf("From() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestErrorUnwrapsToOriginal(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := fmt.Errorf("layer: %w", Wrap(Failure, sentinel))
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is could not reach the wrapped sentinel")
	}
	if err.Error() == "" {
		t.Fatal("error message is empty")
	}
}
