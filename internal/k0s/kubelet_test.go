package k0s

import (
	"errors"
	"reflect"
	"testing"
)

func TestCheckKubelets(t *testing.T) {
	t.Parallel()

	to := mustParse(t, "v1.36.5+k0s.0")

	tests := []struct {
		name        string
		kubelets    []Kubelet
		wantLagging []Kubelet
		wantErr     error
	}{
		{
			name:     "every kubelet on the target minor",
			kubelets: []Kubelet{{"a", "v1.36.4+k0s"}, {"b", "v1.36.5+k0s"}},
		},
		{
			// The forgotten worker on its first minor: legal, and reported.
			name:        "one minor behind",
			kubelets:    []Kubelet{{"w2", "v1.35.9+k0s"}, {"c", "v1.36.4+k0s"}, {"w1", "v1.35.9+k0s"}},
			wantLagging: []Kubelet{{"w1", "v1.35.9+k0s"}, {"w2", "v1.35.9+k0s"}},
		},
		{
			// The same worker at the next minor: what this check exists for.
			name:        "two minors behind",
			kubelets:    []Kubelet{{"w1", "v1.34.12+k0s"}, {"w2", "v1.35.9+k0s"}},
			wantLagging: []Kubelet{{"w2", "v1.35.9+k0s"}},
			wantErr:     ErrSkew,
		},
		{
			name:     "ahead of the target",
			kubelets: []Kubelet{{"w1", "v1.37.1+k0s"}},
			wantErr:  ErrSkew,
		},
		{
			name:     "unreadable version",
			kubelets: []Kubelet{{"w1", "1.36"}},
			wantErr:  ErrMalformedVersion,
		},
		{
			name: "no kubelets",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lagging, err := CheckKubelets(to, test.kubelets)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error is %v, want %v", err, test.wantErr)
			}

			if !reflect.DeepEqual(lagging, test.wantLagging) {
				t.Errorf("lagging = %v, want %v", lagging, test.wantLagging)
			}
		})
	}
}

func TestCheckKubeletsNamesTheStrandedNode(t *testing.T) {
	t.Parallel()

	_, err := CheckKubelets(mustParse(t, "v1.36.5+k0s.0"),
		[]Kubelet{{"forgotten", "v1.34.12+k0s"}})

	want := "unsupported version change: moving a controller to 1.36 would leave " +
		"forgotten (v1.34.12+k0s) outside k0s's skew rules; a worker may be one " +
		"minor behind its controllers and never ahead, so bring them within one " +
		"minor of 1.36 first"

	if err == nil || err.Error() != want {
		t.Errorf("error = %v\nwant %s", err, want)
	}
}

func mustParse(t *testing.T, s string) Version {
	t.Helper()

	v, err := ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}

	return v
}
