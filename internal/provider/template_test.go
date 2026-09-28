package provider

import "testing"

func TestRender(t *testing.T) {
	got, err := Render("mainnet-{height}-{state_hash}.json", map[string]string{
		FieldHeight:    "50000",
		FieldStateHash: "3NLfKan",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "mainnet-50000-3NLfKan.json" {
		t.Errorf("got %q", got)
	}
}

// A placeholder with no value must be an error. Leaving it in the name would
// request a file whose name contains a brace and report a confusing 404.
func TestRenderRefusesAMissingField(t *testing.T) {
	if _, err := Render("mainnet-{height}.json", map[string]string{}); err == nil {
		t.Fatal("expected an error for an unsupplied field")
	}
}

// Render is the second line of defence after the commands check their flags.
// A value that would make the name request another object must be refused.
func TestRenderRefusesUnsafeNames(t *testing.T) {
	tests := []struct {
		name, tmpl, value string
		wantErr           bool
	}{
		{name: "plain ref", tmpl: "{ref}/genesis_ledgers/mainnet.json", value: "compatible", wantErr: false},
		{name: "ref with a slash", tmpl: "{ref}/genesis_ledgers/mainnet.json", value: "release/3.0.0", wantErr: false},
		{name: "dots inside a segment", tmpl: "{ref}/x.json", value: "a..b", wantErr: false},
		{name: "parent segment", tmpl: "{ref}/genesis_ledgers/mainnet.json", value: "../x", wantErr: true},
		{name: "parent segment at the end", tmpl: "d-{ref}", value: "x/..", wantErr: true},
		{name: "only a parent segment", tmpl: "{ref}", value: "..", wantErr: true},
		{name: "query", tmpl: "d-{ref}.sql.tar.gz", value: "2026?x", wantErr: true},
		{name: "fragment", tmpl: "d-{ref}.sql.tar.gz", value: "12#0", wantErr: true},
		{name: "backslash", tmpl: "d-{ref}", value: `a\..\b`, wantErr: true},
		{name: "newline", tmpl: "d-{ref}", value: "a\nb", wantErr: true},
		{name: "nul", tmpl: "d-{ref}", value: "a\x00b", wantErr: true},
		{name: "delete", tmpl: "d-{ref}", value: "a\x7fb", wantErr: true},
		{name: "c1 control", tmpl: "d-{ref}", value: "a\u0085b", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Render(tt.tmpl, map[string]string{FieldRef: tt.value})
			if (err != nil) != tt.wantErr {
				t.Errorf("Render(%q, %q) error = %v, wantErr %v", tt.tmpl, tt.value, err, tt.wantErr)
			}
		})
	}
}

// Every name template in the built-in defaults must still render for valid
// input. A check in Render that refused one would break a default command.
func TestBuiltInTemplatesRender(t *testing.T) {
	cfg, err := parse(defaultYAML)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{
		FieldDate:      "2026-09-22",
		FieldHour:      "2300",
		FieldHeight:    "50000",
		FieldStateHash: "3NLfKanQ53X2MDAENQFgC8fLvjfKLLFEdkzx5fmJgZnvx4tdU7Wv",
		FieldRef:       "release/3.0.0",
	}
	n := 0
	for _, p := range cfg.Providers {
		for _, netw := range p.Networks {
			for _, kind := range []Kind{KindArchiveDump, KindPrecomputedBlocks, KindDaemonConfig, KindGenesisConfig} {
				a := netw.artifact(kind)
				if a == nil || a.Name == "" {
					continue
				}
				n++
				if _, err := Render(a.Name, fields); err != nil {
					t.Errorf("%s: %v", a.Name, err)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no name template found in the built-in defaults")
	}
}

func TestPrefixCutsAtTheFirstUnknownField(t *testing.T) {
	got, err := Prefix("mainnet-{height}-{state_hash}.json", map[string]string{FieldHeight: "50000"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "mainnet-50000-" {
		t.Errorf("got %q, want %q", got, "mainnet-50000-")
	}
}

func TestPrefixWithNoFieldsIsTheFixedPart(t *testing.T) {
	got, err := Prefix("mainnet-archive-dump-{date}_{hour}.sql.tar.gz", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "mainnet-archive-dump-" {
		t.Errorf("got %q", got)
	}
}

func TestCheckTemplate(t *testing.T) {
	if err := CheckTemplate("d-{date}_{hour}", KindArchiveDump); err != nil {
		t.Errorf("valid template rejected: %v", err)
	}
	if err := CheckTemplate("d-{height}", KindArchiveDump); err == nil {
		t.Error("a field the kind does not have should be rejected")
	}
}

func TestMalformedTemplates(t *testing.T) {
	for _, tmpl := range []string{"a-{date", "a-date}", "a-{}"} {
		if err := CheckTemplate(tmpl, KindArchiveDump); err == nil {
			t.Errorf("%q should be rejected", tmpl)
		}
	}
}
