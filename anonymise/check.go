package anonymise

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var (
	timestampForm = `[0-9]{4}-[0-9]{2}-[0-9]{2}t[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(z|[+-][0-9]{2}:[0-9]{2})`
	redactedForm  = `redacted-[0-9]+`
)

// publishedText matches, in lower case, the text the anonymiser and the
// caller's rewrites produce: counter outputs, fixed values, suffixes, the
// caller's vocabulary, timestamps and redacted-N. The leak check removes it
// before looking for a secret, so a one-letter login is not "found" inside
// user12, and a title word "pull" not inside a URL's "/pull/".
func publishedText(r Rules, published []string) (*regexp.Regexp, []string) {
	var words []string
	words = append(words, r.Vocabulary...)
	words = append(words, published...)
	for _, rule := range r.Paths {
		for _, v := range rule.Fixed {
			words = append(words, v)
		}
		words = append(words, rule.Suffixes...)
	}
	var lower []string
	for _, w := range words {
		if w != "" {
			lower = append(lower, strings.ToLower(w))
		}
	}
	sort.Slice(lower, func(i, j int) bool { return len(lower[i]) > len(lower[j]) })
	alts := []string{timestampForm, redactedForm}
	for _, w := range lower {
		alts = append(alts, regexp.QuoteMeta(w))
	}
	return regexp.MustCompile(strings.Join(alts, "|")), lower
}

// checkLeaks walks the anonymised output and refuses it if any value that was
// replaced still appears in it: case-insensitive, URL-decoded, at any length.
// Constants and enums are skipped, being fixed text from the caller rather
// than from the recording; every string is checked for a token marker.
func checkLeaks(r Rules, out any, secrets []secret, published []string) error {
	strip, words := publishedText(r, published)
	return eachString("", out, func(path, s string) error {
		lower := strings.ToLower(s)
		for _, marker := range tokenMarkers {
			if strings.Contains(lower, marker) {
				return fmt.Errorf("anonymise: leak at %s: output holds a token marker", path)
			}
		}
		if c := r.Paths[path].Class; c == Constant || c == Enum {
			return nil
		}
		if kind, found := findSecret(lower, secrets, strip, words); found {
			return fmt.Errorf("anonymise: leak at %s: output holds a %s value", path, kind)
		}
		return nil
	})
}

func findSecret(lower string, secrets []secret, strip *regexp.Regexp, words []string) (string, bool) {
	texts := []string{lower}
	for _, unescape := range []func(string) (string, error){url.QueryUnescape, url.PathUnescape} {
		if u, err := unescape(lower); err == nil && u != lower {
			texts = append(texts, u)
		}
	}
	for _, text := range texts {
		rest := strip.ReplaceAllString(text, " ")
		for _, sec := range secrets {
			v := strings.ToLower(sec.value)
			// A secret that is part of the published text itself ("user",
			// "pull") cannot be told apart from it, so it is looked for only
			// in what is left once that text is removed.
			partOfPublished := slices.ContainsFunc(words, func(w string) bool { return strings.Contains(w, v) })
			if strings.Contains(rest, v) || (!partOfPublished && strings.Contains(text, v)) {
				return sec.kind, true
			}
		}
	}
	return "", false
}

func eachString(path string, v any, f func(path, s string) error) error {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if err := eachString(join(path, k), child, f); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := eachString(path+"[]", child, f); err != nil {
				return err
			}
		}
	case string:
		return f(path, x)
	}
	return nil
}

var (
	publishedTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)
	publishedRedacted  = regexp.MustCompile(`^redacted-[0-9]+$`)
)

// CheckForms refuses doc unless every string in it has the form its path's
// rule publishes: an allowed enum or constant, a UTC timestamp, redacted-N, a
// counter's prefix and number (or one of its fixed values, with any of its
// suffixes), or a match of a rewrite's Form. A path not on the allowlist is
// refused too. It never sees the raw input, so it can run over committed
// files: whatever slipped past the anonymiser, a real name, title or id is in
// none of those forms.
func CheckForms(r Rules, doc []byte) error {
	v, err := decode(doc)
	if err != nil {
		return err
	}
	return eachString("", v, func(path, s string) error {
		rule, listed := r.Paths[path]
		if !listed {
			return fmt.Errorf("anonymise: %s: path is not on the allowlist", path)
		}
		if s == "" || publishedForm(rule, s) {
			return nil
		}
		return fmt.Errorf("anonymise: %s: string is not in the published form of a %s value", path, rule.kind())
	})
}

func publishedForm(rule Rule, s string) bool {
	switch rule.Class {
	case Enum, Constant:
		return slices.Contains(rule.Values, s)
	case Timestamp:
		return publishedTimestamp.MatchString(s)
	case Redact:
		return publishedRedacted.MatchString(s)
	case Counter:
		return CounterForm(rule).MatchString(s)
	case Func:
		return rule.Form != nil && rule.Form.MatchString(s)
	}
	return false
}

// CounterForm is the published form of a counter rule's values, as a regexp
// a caller can embed in a rewrite's Form.
func CounterForm(rule Rule) *regexp.Regexp {
	return regexp.MustCompile(`^` + CounterPattern(rule) + `$`)
}

// CounterPattern is CounterForm without the anchors.
func CounterPattern(rule Rule) string {
	alts := []string{regexp.QuoteMeta(rule.Prefix) + `[0-9]+`}
	for _, v := range rule.Fixed {
		alts = append(alts, regexp.QuoteMeta(v))
	}
	sort.Strings(alts)
	suffix := ""
	if len(rule.Suffixes) > 0 {
		var s []string
		for _, x := range rule.Suffixes {
			s = append(s, regexp.QuoteMeta(x))
		}
		suffix = `(?:` + strings.Join(s, "|") + `)?`
	}
	return `(?:` + strings.Join(alts, "|") + `)` + suffix
}
