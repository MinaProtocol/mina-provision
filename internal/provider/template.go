package provider

import (
	"fmt"
	"sort"
	"strings"
)

// Template fields. Making the naming rule data rather than code is what lets a
// second publisher use its own scheme: pluggable buckets alone would not be
// enough, because the file names differ too.
const (
	FieldDate      = "date"       // archive dump, YYYY-MM-DD
	FieldHour      = "hour"       // archive dump, HHMM
	FieldHeight    = "height"     // precomputed block
	FieldStateHash = "state_hash" // precomputed block
	FieldRef       = "ref"        // config fetched from a source tree
)

// allowedFields says which fields each artifact kind may use. A template that
// names anything else is rejected when the configuration is read, not when a
// download fails.
var allowedFields = map[Kind][]string{
	KindArchiveDump:       {FieldDate, FieldHour},
	KindPrecomputedBlocks: {FieldHeight, FieldStateHash},
	KindConfig:            {FieldRef},
}

// AllowedFields lists the fields a kind may use, for error messages.
func AllowedFields(kind Kind) []string {
	f := append([]string(nil), allowedFields[kind]...)
	sort.Strings(f)
	return f
}

// CheckTemplate reports whether every placeholder in tmpl is allowed for kind.
func CheckTemplate(tmpl string, kind Kind) error {
	names, err := placeholders(tmpl)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, f := range allowedFields[kind] {
		allowed[f] = true
	}
	for _, n := range names {
		if !allowed[n] {
			return fmt.Errorf("name template uses {%s}, which %s does not have (it has: %s)",
				n, kind, strings.Join(AllowedFields(kind), ", "))
		}
	}
	return nil
}

// Render substitutes every placeholder in tmpl from fields. A placeholder with
// no value is an error: silently leaving it in the name would produce a
// request for a file whose name contains a brace, and a confusing 404.
func Render(tmpl string, fields map[string]string) (string, error) {
	names, err := placeholders(tmpl)
	if err != nil {
		return "", err
	}
	out := tmpl
	for _, n := range names {
		v, ok := fields[n]
		if !ok {
			return "", fmt.Errorf("name template needs {%s}, which was not supplied", n)
		}
		out = strings.ReplaceAll(out, "{"+n+"}", v)
	}
	return out, nil
}

// Prefix renders the fields that are known and cuts the template at the first
// placeholder that is not, giving the longest fixed prefix of the name.
//
// Finding a block by height needs this: the height is known and the state hash
// is not, so the name can only be narrowed to a prefix and the rest listed.
func Prefix(tmpl string, fields map[string]string) (string, error) {
	names, err := placeholders(tmpl)
	if err != nil {
		return "", err
	}
	out := tmpl
	for _, n := range names {
		if v, ok := fields[n]; ok {
			out = strings.ReplaceAll(out, "{"+n+"}", v)
		}
	}
	if i := strings.Index(out, "{"); i >= 0 {
		return out[:i], nil
	}
	return out, nil
}

// placeholders returns the names in braces, in order of first appearance, and
// rejects an unbalanced brace rather than treating it as a literal.
func placeholders(tmpl string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	rest := tmpl
	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			if strings.Contains(rest, "}") {
				return nil, fmt.Errorf("name template %q has a closing brace with no opening brace", tmpl)
			}
			return names, nil
		}
		close := strings.Index(rest[open:], "}")
		if close < 0 {
			return nil, fmt.Errorf("name template %q has an unclosed placeholder", tmpl)
		}
		name := rest[open+1 : open+close]
		if name == "" {
			return nil, fmt.Errorf("name template %q has an empty placeholder", tmpl)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		rest = rest[open+close+1:]
	}
}
