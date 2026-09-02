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
