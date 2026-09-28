package cmd

import (
	"strings"
	"testing"
)

// The valid values are real branch and tag names of the mina repository, and
// a commit hash.
func TestValidateRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{name: "default branch", ref: "compatible", wantErr: false},
		{name: "branch with a slash", ref: "release/3.0.0", wantErr: false},
		{name: "tag", ref: "3.0.3", wantErr: false},
		{name: "prerelease tag", ref: "3.1.0-alpha1", wantErr: false},
		{name: "commit", ref: "bd0fe9e9a1f4c0b0e4ad53e2a2e2c9b5d0b1a2c3", wantErr: false},
		{name: "branch with a plus", ref: "compatible+develop", wantErr: false},
		{name: "branch with an at", ref: "al@runtime-config", wantErr: false},
		{name: "dots inside a segment", ref: "a..b", wantErr: false},

		{name: "empty", ref: "", wantErr: true},
		{name: "parent segment", ref: "../x", wantErr: true},
		{name: "parent segment inside", ref: "compatible/../x", wantErr: true},
		{name: "parent segment at the end", ref: "compatible/..", wantErr: true},
		{name: "only a parent segment", ref: "..", wantErr: true},
		{name: "leading slash", ref: "/compatible", wantErr: true},
		{name: "leading dash", ref: "-compatible", wantErr: true},
		{name: "query", ref: "compatible?x=1", wantErr: true},
		{name: "fragment", ref: "berkeley#15325", wantErr: true},
		{name: "percent", ref: "a%2e%2e", wantErr: true},
		{name: "backslash", ref: `a\b`, wantErr: true},
		{name: "space", ref: "a b", wantErr: true},
		{name: "newline", ref: "a\nb", wantErr: true},
		{name: "colon", ref: "https://x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRef(tt.ref)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRef(%q) error = %v, wantErr %v", tt.ref, err, tt.wantErr)
			}
		})
	}
}

// An invalid --ref must stop the command before any request, and the message
// must name the flag.
func TestDaemonConfigRejectsInvalidRefBeforeAnyRequest(t *testing.T) {
	for _, ref := range []string{"../x", "compatible?x=1", "berkeley#15325", "-x"} {
		t.Run(ref, func(t *testing.T) {
			requests := useCountingProvider(t)
			setFlag(t, &configOut, t.TempDir())
			setFlag(t, &configRef, ref)

			err := runConfig(nil, nil)
			if err == nil || !strings.Contains(err.Error(), "--ref") {
				t.Fatalf("err = %v, want an error naming --ref", err)
			}
			if n := requests.Load(); n != 0 {
				t.Errorf("%d requests were made, want none", n)
			}
		})
	}
}

// The control case for the test above: a valid ref reaches the provider. The
// server answers 404, so the command then fails.
func TestDaemonConfigWithValidRefReachesTheProvider(t *testing.T) {
	requests := useCountingProvider(t)
	setFlag(t, &configOut, t.TempDir())
	setFlag(t, &configRef, "compatible+develop")

	if err := runConfig(nil, nil); err == nil {
		t.Fatal("expected the fetch to fail with 404")
	}
	if n := requests.Load(); n == 0 {
		t.Error("no request was made; the counting provider is not in use")
	}
}
