// Package anonymise turns a recorded JSON document - requests to and
// responses from some API - into one that can be published.
//
// It is shared: elly uses it for its golden tests, and other modules import it
// for theirs. It holds no policy and knows no forge. Each caller supplies its
// Rules: the allowlist keyed by JSON path, saying what kind of value sits at
// each path, which values an enum or a constant may hold, which raw values
// map to fixed published ones (the recording user to "me", a bot kept
// verbatim) and, through Rewrite functions, how a composite value such as a
// URL or a search string is rebuilt. A value at a path the caller did not list
// is refused, never passed through.
//
// What the package provides is the mechanism: the walk, one mapping table with
// per-document counters (the same raw value always gives the same published
// one, in requests and responses alike), one random time offset for every
// timestamp that is never stored, and two leak checks. Errors name the JSON
// path and the kind of a value, never the value.
package anonymise

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Class is how a value is anonymised.
type Class int

const (
	// Keep is for numbers and booleans. A string at a Keep path is refused.
	Keep Class = iota + 1
	// Enum must be one of Rule.Values and is kept as is.
	Enum
	// Constant must be one of Rule.Values and is kept as is. Unlike an enum
	// it may be long, e.g. a query text.
	Constant
	// Timestamp is RFC 3339 and is shifted by the document's offset.
	Timestamp
	// Counter maps each distinct raw value to Rule.Prefix plus a number.
	Counter
	// Redact maps each distinct raw value to redacted-N.
	Redact
	// Func rebuilds the value with Rule.Rewrite.
	Func
)

var classNames = map[Class]string{
	Keep: "keep", Enum: "enum", Constant: "constant", Timestamp: "timestamp",
	Counter: "counter", Redact: "redacted", Func: "rewritten",
}

// Rule says what the value at one path is.
type Rule struct {
	Class Class
	// Kind names the value in errors, e.g. "login"; the class name otherwise.
	Kind string
	// Values are what an Enum or a Constant may hold.
	Values []string
	// Prefix, Format and Sorted shape a Counter: Prefix plus the number in
	// Format ("%d" when empty). Sorted numbers the raw values in their sorted
	// order across the whole document, so mapped values sort as the raw ones
	// did - for a caller that sorts them, say before batching.
	Prefix string
	Format string
	Sorted bool
	// Pattern, when set, is what a raw Counter value must match.
	Pattern *regexp.Regexp
	// Fixed maps raw Counter values to fixed published ones, e.g. the
	// recording user's login to "me". A value mapped to itself is kept
	// verbatim; any other is still treated as a secret.
	Fixed map[string]string
	// Suffixes are kept on a Counter value that ends in one, the rest of the
	// value being mapped on its own, e.g. "[bot]".
	Suffixes []string
	// Rewrite rebuilds a Func value, typically by applying other rules to its
	// parts through the Mapper.
	Rewrite func(m *Mapper, raw string) (string, error)
	// Form is what a published Func value looks like, for CheckForms.
	Form *regexp.Regexp
}

func (r Rule) kind() string {
	if r.Kind != "" {
		return r.Kind
	}
	if n, ok := classNames[r.Class]; ok {
		return n
	}
	return fmt.Sprintf("class %d", int(r.Class))
}

// As is the rule for a class that needs nothing else.
func As(c Class) Rule { return Rule{Class: c} }

// EnumOf is an Enum rule allowing exactly values.
func EnumOf(values ...string) Rule { return Rule{Class: Enum, Values: values} }

// ConstantOf is a Constant rule allowing exactly values.
func ConstantOf(values ...string) Rule { return Rule{Class: Constant, Values: values} }

// Rules is a caller's policy for one kind of document.
type Rules struct {
	// Paths maps a JSON path to the rule for the value there. Object keys
	// are joined with "." and an array element is "[]", e.g.
	// "data.items[].title".
	Paths map[string]Rule
	// Vocabulary is literal text the caller's rewrites put into published
	// values, such as a URL's host and fixed path segments. The leak check
	// removes it before looking for a secret, so a title word that happens
	// to be one of these does not read as a leak.
	Vocabulary []string
}

// tokenMarkers are prefixes of common API tokens and the Authorization
// scheme. A token never belongs in a body, but if one ever got there it must
// not be published, whatever rule let it through.
var tokenMarkers = []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-", "bearer"}

// Anonymise returns doc with every value replaced according to r, or an error
// naming the first path that is not on the allowlist or does not hold what r
// says it does. The output is indented JSON.
func Anonymise(r Rules, doc []byte) ([]byte, error) {
	v, err := decode(doc)
	if err != nil {
		return nil, err
	}
	offset, err := randomOffset()
	if err != nil {
		return nil, err
	}
	m := &Mapper{offset: offset, table: map[string]string{}, counters: map[string]int{}}
	known := prefixes(r.Paths)
	sorted := map[string]map[string]bool{}
	if err := validate(r, known, "", v, sorted); err != nil {
		return nil, err
	}
	m.assignSorted(r, sorted)
	out, err := m.walk(r, "", v)
	if err != nil {
		return nil, err
	}
	if err := checkLeaks(r, out, m.secrets, m.published); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("anonymise: %w", err)
	}
	return append(b, '\n'), nil
}

func decode(doc []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, errors.New("anonymise: document is not JSON")
	}
	return v, nil
}

// offsetSeconds is how many distinct offsets randomOffset draws from.
const offsetSeconds = int64(700 * 24 * time.Hour / time.Second)

// randomOffset is how far every timestamp moves back: between 30 days and two
// years, plus a random time of day, so neither the date nor the hour of the
// recording survives.
func randomOffset() (time.Duration, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(offsetSeconds))
	if err != nil {
		return 0, fmt.Errorf("anonymise: no randomness for the time offset: %w", err)
	}
	return offsetFor(n.Int64()), nil
}

// offsetFor maps a draw in [0, offsetSeconds) to an offset strictly more than
// 30 days and at most 730 days back.
func offsetFor(n int64) time.Duration {
	return -(30*24*time.Hour + time.Duration(n+1)*time.Second)
}

type secret struct {
	value string
	kind  string
}

// Mapper holds one document's mapping table and counters. A Rewrite function
// receives it to map the parts of a composite value.
type Mapper struct {
	offset    time.Duration
	table     map[string]string // namespace NUL raw value -> published value
	counters  map[string]int
	secrets   []secret
	published []string // counter outputs, removed before the leak check
}

func prefixes(paths map[string]Rule) map[string]bool {
	out := map[string]bool{}
	for p := range paths {
		for i := range p {
			if p[i] == '.' || p[i] == '[' {
				out[p[:i]] = true
			}
		}
	}
	return out
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// validate refuses any path that is neither on the allowlist nor leads to one,
// and collects the raw values of sorted counters.
func validate(r Rules, known map[string]bool, path string, v any, sorted map[string]map[string]bool) error {
	rule, listed := r.Paths[path]
	unlisted := fmt.Errorf("anonymise: %s: path is not on the allowlist", path)
	switch x := v.(type) {
	case map[string]any:
		if path != "" && !known[path] {
			return unlisted
		}
		for k, child := range x {
			if err := validate(r, known, join(path, k), child, sorted); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := validate(r, known, path+"[]", child, sorted); err != nil {
				return err
			}
		}
	case nil:
		if !listed && !known[path] {
			return unlisted
		}
	default:
		if !listed {
			return unlisted
		}
		if s, ok := x.(string); ok && rule.Class == Counter && rule.Sorted && s != "" {
			if sorted[rule.Prefix] == nil {
				sorted[rule.Prefix] = map[string]bool{}
			}
			sorted[rule.Prefix][s] = true
		}
	}
	return nil
}

func (m *Mapper) assignSorted(r Rules, sorted map[string]map[string]bool) {
	rules := map[string]Rule{}
	for _, rule := range r.Paths {
		if rule.Class == Counter && rule.Sorted {
			rules[rule.Prefix] = rule
		}
	}
	for prefix, set := range sorted {
		raws := make([]string, 0, len(set))
		for raw := range set {
			if _, fixed := rules[prefix].Fixed[raw]; !fixed {
				raws = append(raws, raw)
			}
		}
		sort.Strings(raws)
		for _, raw := range raws {
			m.count(rules[prefix], raw)
		}
	}
}

func (m *Mapper) walk(r Rules, path string, v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		// sorted, so counters do not follow Go's random map iteration order
		for _, k := range slices.Sorted(maps.Keys(x)) {
			child := x[k]
			mapped, err := m.walk(r, join(path, k), child)
			if err != nil {
				return nil, err
			}
			out[k] = mapped
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(x))
		for _, child := range x {
			mapped, err := m.walk(r, path+"[]", child)
			if err != nil {
				return nil, err
			}
			out = append(out, mapped)
		}
		return out, nil
	case string:
		out, err := m.Apply(r.Paths[path], x)
		if err != nil {
			return nil, fmt.Errorf("anonymise: %s: %w", path, err)
		}
		return out, nil
	default:
		// numbers, booleans and null at a listed path carry no names
		return x, nil
	}
}

// Apply anonymises raw by rule. Its errors name the kind, never the value.
func (m *Mapper) Apply(rule Rule, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	refuse := func(what string) (string, error) {
		return "", fmt.Errorf("%s value %s", rule.kind(), what)
	}
	switch rule.Class {
	case Enum:
		if !slices.Contains(rule.Values, raw) {
			return refuse("is not on the enum list")
		}
		return raw, nil
	case Constant:
		if !slices.Contains(rule.Values, raw) {
			return refuse("is not one of the registered constants")
		}
		return raw, nil
	case Timestamp:
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return refuse("is not an RFC 3339 timestamp")
		}
		return t.Add(m.offset).UTC().Format(time.RFC3339), nil
	case Counter:
		if rule.Pattern != nil && !rule.Pattern.MatchString(raw) {
			return refuse("does not have the expected form")
		}
		return m.count(rule, raw), nil
	case Redact:
		return m.redact(rule, raw), nil
	case Func:
		if rule.Rewrite == nil {
			return refuse("has no rewrite")
		}
		return rule.Rewrite(m, raw)
	}
	// Keep, or no rule at all: a string here has nothing saying what it is
	return refuse("is a string where none is expected")
}

func (m *Mapper) count(rule Rule, raw string) string {
	if out, ok := rule.Fixed[raw]; ok {
		if out != raw {
			m.Secret(raw, rule.kind())
		}
		return out
	}
	for _, suffix := range rule.Suffixes {
		if base, ok := strings.CutSuffix(raw, suffix); ok && base != "" {
			return m.count(rule, base) + suffix
		}
	}
	key := rule.Prefix + "\x00" + raw
	if out, ok := m.table[key]; ok {
		return out
	}
	format := rule.Format
	if format == "" {
		format = "%d"
	}
	out := rule.Prefix + fmt.Sprintf(format, m.Number(rule.Prefix, ""))
	m.table[key] = out
	m.Secret(raw, rule.kind())
	m.published = append(m.published, out)
	return out
}

// Number returns the next number in namespace for a new key, or the number
// key already got: per-document renumbering for numbers inside a composite
// value, such as an issue number in a URL. An empty key always takes the next
// number. Keys are not secrets; register the parts that are with Secret.
func (m *Mapper) Number(namespace, key string) int {
	tableKey := "\x00#" + namespace + "\x00" + key
	if key != "" {
		if out, ok := m.table[tableKey]; ok {
			var n int
			_, _ = fmt.Sscan(out, &n)
			return n
		}
	}
	m.counters[namespace]++
	n := m.counters[namespace]
	if key != "" {
		m.table[tableKey] = fmt.Sprint(n)
	}
	return n
}

// Secret registers a raw value that must not appear in the output.
func (m *Mapper) Secret(raw, kind string) {
	if raw != "" {
		m.secrets = append(m.secrets, secret{raw, kind})
	}
}

// redact replaces free text, and remembers its longer words as secrets too:
// a title fragment surviving elsewhere is as much a leak as the whole title.
func (m *Mapper) redact(rule Rule, raw string) string {
	key := "\x00redacted\x00" + raw
	if out, ok := m.table[key]; ok {
		return out
	}
	for _, w := range strings.FieldsFunc(raw, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 4 && strings.ContainsFunc(w, unicode.IsLetter) {
			m.Secret(w, rule.kind())
		}
	}
	out := fmt.Sprintf("redacted-%d", m.Number("\x00redacted", ""))
	m.table[key] = out
	m.Secret(raw, rule.kind())
	return out
}
