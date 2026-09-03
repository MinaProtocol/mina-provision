package cmd

import (
	"testing"

	"github.com/MinaProtocol/mina-provision/internal/provider"
)

// The names the default provider produces are the ones the Mina Foundation
// actually publishes. Moving the naming rule into configuration must not have
// changed them, so the expectations here are the names the tool built before
// the rule became data.
func TestDefaultProviderProducesTheKnownNames(t *testing.T) {
	cfg, path, err := provider.Load("")
	if err != nil {
		t.Fatalf("load built-in defaults: %v", err)
	}
	if path != "" {
		t.Skipf("a user configuration at %s is in effect", path)
	}

	tests := []struct {
		network string
		kind    provider.Kind
		fields  map[string]string
		want    string
	}{
		{
			network: "mainnet",
			kind:    provider.KindArchiveDump,
			fields:  map[string]string{provider.FieldDate: "2026-06-23", provider.FieldHour: "0000"},
			want:    "mainnet-archive-dump-2026-06-23_0000.sql.tar.gz",
		},
		{
			network: "devnet",
			kind:    provider.KindArchiveDump,
			fields:  map[string]string{provider.FieldDate: "2026-01-02", provider.FieldHour: "1200"},
			want:    "devnet-archive-dump-2026-01-02_1200.sql.tar.gz",
		},
	}
	for _, tt := range tests {
		art, err := cfg.Resolve("", tt.network, tt.kind)
		if err != nil {
			t.Fatalf("resolve %s/%s: %v", tt.network, tt.kind, err)
		}
		got, err := provider.Render(art.Name, tt.fields)
		if err != nil {
			t.Fatalf("render %s/%s: %v", tt.network, tt.kind, err)
		}
		if got != tt.want {
			t.Errorf("%s %s = %q, want %q", tt.network, tt.kind, got, tt.want)
		}
	}
}

// A height alone cannot name a block, because the state hash is part of the
// name. What it can do is narrow the name to a prefix to list.
func TestBlockPrefixForHeight(t *testing.T) {
	cfg, _, err := provider.Load("")
	if err != nil {
		t.Fatal(err)
	}
	art, err := cfg.Resolve("", "mainnet", provider.KindPrecomputedBlocks)
	if err != nil {
		t.Fatal(err)
	}
	got, err := provider.Prefix(art.Name, map[string]string{provider.FieldHeight: "50000"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "mainnet-50000-" {
		t.Errorf("prefix = %q, want %q", got, "mainnet-50000-")
	}
}

func TestValidateHour(t *testing.T) {
	tests := []struct {
		name    string
		hour    string
		wantErr bool
	}{
		{name: "midnight", hour: "0000", wantErr: false},
		{name: "noon", hour: "1200", wantErr: false},
		{name: "late", hour: "2359", wantErr: false},
		{name: "non-digit but 4 chars accepted", hour: "abcd", wantErr: false},
		{name: "too short", hour: "000", wantErr: true},
		{name: "too long", hour: "00000", wantErr: true},
		{name: "empty", hour: "", wantErr: true},
		{name: "single", hour: "0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHour(tt.hour)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateHour(%q) error = %v, wantErr %v", tt.hour, err, tt.wantErr)
			}
		})
	}
}
