// Package provider holds the description of where artifacts come from.
//
// Every endpoint, bucket and file-naming rule is data, not code. The Mina
// Foundation is the default publisher of these artifacts, not the only
// possible one: an operator points --provider at a mirror, an internal store
// or a local directory without changing the program.
//
// The built-in defaults are the same schema an operator writes, embedded in
// the binary and parsed by this same reader, so a custom provider can express
// everything the defaults express.
//
// The configuration is read from disk only. It is never fetched: it states
// which hosts may supply artifacts, so downloading it would remove the
// property that makes it worth having.
package provider

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var defaultYAML []byte

// Kind names an artifact a provider can publish.
type Kind string

const (
	KindArchiveDump       Kind = "archive_dump"
	KindPrecomputedBlocks Kind = "precomputed_blocks"
	KindConfig            Kind = "config"
)

// Config is a whole provider file.
type Config struct {
	Version         int                 `yaml:"version"`
	DefaultProvider string              `yaml:"default_provider"`
	Providers       map[string]Provider `yaml:"providers"`
}

// Provider is one publisher of artifacts.
type Provider struct {
	Description string             `yaml:"description"`
	Networks    map[string]Network `yaml:"networks"`
}

// Network is what one provider publishes for one Mina network.
type Network struct {
	ArchiveDump       *Artifact `yaml:"archive_dump,omitempty"`
	PrecomputedBlocks *Artifact `yaml:"precomputed_blocks,omitempty"`
	Config            *Artifact `yaml:"config,omitempty"`
}

// Artifact says where one kind of file lives and how it is named.
type Artifact struct {
	// Backend selects the transport: gcs, http, file or apt.
	Backend string `yaml:"backend"`

	Bucket  string `yaml:"bucket,omitempty"`   // gcs
	BaseURL string `yaml:"base_url,omitempty"` // http
	Path    string `yaml:"path,omitempty"`     // file

	// Name is the object name, as a template over the fields allowed for the
	// artifact kind, for example "mainnet-{height}-{state_hash}.json".
	Name string `yaml:"name,omitempty"`

	// Index is an optional URL serving one object name per line. The http
	// backend cannot enumerate a remote directory, so discovery by prefix --
	// which finding a block by height needs -- is possible only with it.
	Index string `yaml:"index,omitempty"`

	// Checksum states how the download is verified: index, sidecar or none.
	Checksum string `yaml:"checksum,omitempty"`

	Repository string `yaml:"repository,omitempty"` // apt
	Codename   string `yaml:"codename,omitempty"`   // apt
	Component  string `yaml:"component,omitempty"`  // apt
	Package    string `yaml:"package,omitempty"`    // apt
}

// Checksum modes.
const (
	ChecksumIndex   = "index"
	ChecksumSidecar = "sidecar"
	ChecksumNone    = "none"
)

// Backends.
const (
	BackendGCS  = "gcs"
	BackendHTTP = "http"
	BackendFile = "file"
	BackendAPT  = "apt"
)

// SearchPaths lists where a user configuration is looked for, in order. The
// first file that exists is used; it is merged over the built-in defaults.
func SearchPaths() []string {
	paths := []string{"mina-provision.yaml"}
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "mina-provision", "config.yaml"))
	}
	return append(paths, "/etc/mina-provision/config.yaml")
}

// Load reads the configuration. explicitPath, when set, is the only file
// consulted and must exist; otherwise the environment variable
// MINA_PROVISION_CONFIG and then SearchPaths are tried. The result is always
// the built-in defaults with the user file merged over them.
//
// The second return value names the file that was used, for logging, or is
// empty when only the defaults applied.
func Load(explicitPath string) (*Config, string, error) {
	base, err := parse(defaultYAML)
	if err != nil {
		return nil, "", fmt.Errorf("built-in defaults: %w", err)
	}

	path := explicitPath
	if path == "" {
		path = os.Getenv("MINA_PROVISION_CONFIG")
	}
	if path != "" {
		over, err := parseFile(path)
		if err != nil {
			return nil, "", err
		}
		merged := merge(*base, *over)
		return &merged, path, validate(&merged)
	}

	for _, candidate := range SearchPaths() {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		over, err := parseFile(candidate)
		if err != nil {
			return nil, "", err
		}
		merged := merge(*base, *over)
		return &merged, candidate, validate(&merged)
	}
	return base, "", validate(base)
}

func parseFile(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read provider config: %w", err)
	}
	c, err := parse(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func parse(body []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	// A misspelt key would otherwise be dropped in silence, and the operator
	// would see the default endpoint used instead of the one they wrote.
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &c, nil
}

// merge overlays over onto base at three levels: provider, network, then
// artifact. An artifact present in over replaces the base one entirely, so a
// partial artifact cannot inherit half of an endpoint it was meant to
// replace. Everything the user did not mention keeps coming from the
// defaults, which is what lets a mirror be added without restating them.
func merge(base, over Config) Config {
	out := Config{
		Version:         base.Version,
		DefaultProvider: base.DefaultProvider,
		Providers:       map[string]Provider{},
	}
	if over.Version != 0 {
		out.Version = over.Version
	}
	if over.DefaultProvider != "" {
		out.DefaultProvider = over.DefaultProvider
	}
	for name, p := range base.Providers {
		out.Providers[name] = p
	}
	for name, op := range over.Providers {
		bp, exists := out.Providers[name]
		if !exists {
			out.Providers[name] = op
			continue
		}
		merged := Provider{Description: bp.Description, Networks: map[string]Network{}}
		if op.Description != "" {
			merged.Description = op.Description
		}
		for n, bn := range bp.Networks {
			merged.Networks[n] = bn
		}
		for n, on := range op.Networks {
			bn, ok := merged.Networks[n]
			if !ok {
				merged.Networks[n] = on
				continue
			}
			if on.ArchiveDump != nil {
				bn.ArchiveDump = on.ArchiveDump
			}
			if on.PrecomputedBlocks != nil {
				bn.PrecomputedBlocks = on.PrecomputedBlocks
			}
			if on.Config != nil {
				bn.Config = on.Config
			}
			merged.Networks[n] = bn
		}
		out.Providers[name] = merged
	}
	return out
}

// Resolve returns the artifact of the given kind that a provider publishes for
// a network. An empty providerName selects the configured default.
func (c *Config) Resolve(providerName, networkName string, kind Kind) (*Artifact, error) {
	if providerName == "" {
		providerName = c.DefaultProvider
	}
	p, ok := c.Providers[providerName]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q; configured providers: %s",
			providerName, strings.Join(c.ProviderNames(), ", "))
	}
	n, ok := p.Networks[networkName]
	if !ok {
		return nil, fmt.Errorf("provider %q publishes nothing for network %q; it has: %s",
			providerName, networkName, strings.Join(sortedKeys(p.Networks), ", "))
	}
	a := n.artifact(kind)
	if a == nil {
		return nil, fmt.Errorf("provider %q publishes no %s for network %q",
			providerName, kind, networkName)
	}
	return a, nil
}

func (n Network) artifact(kind Kind) *Artifact {
	switch kind {
	case KindArchiveDump:
		return n.ArchiveDump
	case KindPrecomputedBlocks:
		return n.PrecomputedBlocks
	case KindConfig:
		return n.Config
	default:
		return nil
	}
}

// ProviderNames lists the configured providers.
func (c *Config) ProviderNames() []string { return sortedKeys(c.Providers) }

// Networks lists the networks a provider publishes for.
func (c *Config) Networks(providerName string) []string {
	if providerName == "" {
		providerName = c.DefaultProvider
	}
	p, ok := c.Providers[providerName]
	if !ok {
		return nil
	}
	return sortedKeys(p.Networks)
}

// validate rejects a configuration that would only fail later, at fetch time,
// with a message pointing at a URL rather than at the line that is wrong.
func validate(c *Config) error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d (this build understands version 1)", c.Version)
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("no providers defined")
	}
	if _, ok := c.Providers[c.DefaultProvider]; !ok {
		return fmt.Errorf("default_provider %q is not defined; providers: %s",
			c.DefaultProvider, strings.Join(c.ProviderNames(), ", "))
	}
	for pn, p := range c.Providers {
		for nn, n := range p.Networks {
			for _, kind := range []Kind{KindArchiveDump, KindPrecomputedBlocks, KindConfig} {
				a := n.artifact(kind)
				if a == nil {
					continue
				}
				if err := a.validate(kind); err != nil {
					return fmt.Errorf("provider %s, network %s, %s: %w", pn, nn, kind, err)
				}
			}
		}
	}
	return nil
}

func (a *Artifact) validate(kind Kind) error {
	switch a.Backend {
	case BackendGCS:
		if a.Bucket == "" {
			return fmt.Errorf("backend gcs needs a bucket")
		}
	case BackendHTTP:
		if a.BaseURL == "" {
			return fmt.Errorf("backend http needs a base_url")
		}
	case BackendFile:
		if a.Path == "" {
			return fmt.Errorf("backend file needs a path")
		}
	case BackendAPT:
		if a.Repository == "" || a.Package == "" {
			return fmt.Errorf("backend apt needs a repository and a package")
		}
		if kind != KindConfig {
			return fmt.Errorf("backend apt is only meaningful for the config artifact")
		}
	case "":
		return fmt.Errorf("no backend given (one of gcs, http, file, apt)")
	default:
		return fmt.Errorf("unknown backend %q (one of gcs, http, file, apt)", a.Backend)
	}

	switch a.Checksum {
	case ChecksumIndex, ChecksumSidecar, ChecksumNone, "":
	default:
		return fmt.Errorf("unknown checksum mode %q (one of index, sidecar, none)", a.Checksum)
	}
	if a.Checksum == ChecksumIndex && a.Backend != BackendAPT {
		return fmt.Errorf("checksum: index needs an index to read, which only the apt backend has")
	}

	if a.Backend != BackendAPT {
		if a.Name == "" {
			return fmt.Errorf("no name template given")
		}
		if err := CheckTemplate(a.Name, kind); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
