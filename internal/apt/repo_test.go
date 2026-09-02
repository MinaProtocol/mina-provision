package apt

import (
	"strings"
	"testing"
)

const sampleIndex = `Package: mina-mainnet-config
Version: 3.4.0-bd0fe9e
Architecture: all
Filename: pool/noble/m/mi/mina-mainnet-config_3.4.0-bd0fe9e_all.deb
Size: 1574
SHA256: 1C7313C8DA63EFBF4C0DDB113DA03F9CA6BFD5D59669FB535ABE485F41879EA2
Description: Mina Protocol Config for daemons running under mainnet
 Built from bd0fe9e by buildkite

Package: mina-mainnet
Version: 3.4.0-bd0fe9e
Architecture: amd64
Filename: pool/noble/m/mi/mina-mainnet_3.4.0-bd0fe9e_amd64.deb
Size: 100
SHA256: aa

Package: mina-mainnet-config
Version: 3.3.1-7b34378
Architecture: all
Filename: pool/noble/m/mi/mina-mainnet-config_3.3.1-7b34378_all.deb
Size: 1500
SHA256: bb
`

func TestParsePackagesSelectsByNameAndReadsFields(t *testing.T) {
	pkgs, err := parsePackages(strings.NewReader(sampleIndex), "mina-mainnet-config")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %d stanzas, want 2: %+v", len(pkgs), pkgs)
	}
	p := pkgs[0]
	if p.Version != "3.4.0-bd0fe9e" || p.Size != 1574 || p.Architecture != "all" {
		t.Errorf("unexpected first stanza: %+v", p)
	}
	if p.Filename != "pool/noble/m/mi/mina-mainnet-config_3.4.0-bd0fe9e_all.deb" {
		t.Errorf("unexpected filename: %q", p.Filename)
	}
	// The checksum is lowercased so the comparison against a computed digest
	// does not depend on how the repository happened to print it.
	if p.SHA256 != "1c7313c8da63efbf4c0ddb113da03f9ca6bfd5d59669fb535abe485f41879ea2" {
		t.Errorf("checksum not normalised: %q", p.SHA256)
	}
}

func TestParsePackagesIgnoresContinuationLines(t *testing.T) {
	pkgs, err := parsePackages(strings.NewReader(sampleIndex), "mina-mainnet-config")
	if err != nil {
		t.Fatal(err)
	}
	// "Built from ..." is a continuation of Description and must not become a
	// field of its own or leak into the next stanza.
	for _, p := range pkgs {
		if strings.Contains(p.Filename, "Built from") {
			t.Errorf("continuation line leaked into %+v", p)
		}
	}
}
