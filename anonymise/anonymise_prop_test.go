package anonymise

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Raw values come from an alphabet that no published form and no enum value
// uses, so "this raw value appears in the output" can only mean a leak, never
// a coincidence with user12 or redacted-3.
var (
	rawValue  = rapid.StringMatching(`[QXZJKqxzjk]{3,8}`)
	enumValue = rapid.StringMatching(`E[EFGH]{2,5}`)
	propSelf  = "zzqqzz"
	propBase  = time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
)

type propField struct {
	key   string
	rule  Rule
	array bool // the field holds a list of values rather than one
}

// genRules draws a rule per field: the keep-list (enums), counters with and
// without fixed values, sorted counters, redaction, timestamps and numbers.
func genRules(t *rapid.T, enums []string) []propField {
	catalogue := []Rule{
		{Class: Counter, Kind: "login", Prefix: "user", Fixed: map[string]string{propSelf: "me"}, Suffixes: []string{"[bot]"}},
		{Class: Counter, Kind: "repo", Prefix: "repo"},
		{Class: Counter, Kind: "node id", Prefix: "ID_", Format: "%06d", Sorted: true},
		As(Redact),
		EnumOf(enums...),
		As(Timestamp),
		As(Keep),
	}
	n := rapid.IntRange(1, 6).Draw(t, "fields")
	fields := make([]propField, n)
	for i := range fields {
		fields[i] = propField{
			key:   fmt.Sprintf("f%d", i),
			rule:  rapid.SampledFrom(catalogue).Draw(t, fmt.Sprintf("rule%d", i)),
			array: rapid.Bool().Draw(t, fmt.Sprintf("array%d", i)),
		}
	}
	return fields
}

func propRules(fields []propField) Rules {
	r := Rules{Paths: map[string]Rule{}}
	for _, f := range fields {
		p := "items[]." + f.key
		if f.array {
			p += "[]"
		}
		r.Paths[p] = f.rule
	}
	return r
}

// leaf is one input value with the path and rule it was generated for.
type leaf struct {
	item  int
	field propField
	index int
	raw   any
}

// genDocument draws items whose string values repeat across items and fields,
// so consistency is actually exercised.
func genDocument(t *rapid.T, fields []propField, enums []string) (map[string]any, []leaf) {
	pool := rapid.SliceOfNDistinct(rawValue, 1, 6, strings.ToLower).Draw(t, "pool")
	pool = append(pool, propSelf)
	var leaves []leaf
	items := []any{}
	for i := range rapid.IntRange(1, 5).Draw(t, "items") {
		item := map[string]any{}
		for _, f := range fields {
			value := func(j int) any {
				var v any
				switch f.rule.Class {
				case Enum:
					v = rapid.SampledFrom(enums).Draw(t, "enum")
				case Timestamp:
					v = propBase.Add(time.Duration(rapid.IntRange(-1e7, 1e7).Draw(t, "seconds")) * time.Second).Format(time.RFC3339)
				case Keep:
					v = rapid.IntRange(0, 1000).Draw(t, "number")
				default:
					v = rapid.SampledFrom(pool).Draw(t, "raw")
				}
				leaves = append(leaves, leaf{i, f, j, v})
				return v
			}
			if f.array {
				var list []any
				for j := range rapid.IntRange(0, 3).Draw(t, "len") {
					list = append(list, value(j))
				}
				item[f.key] = list
			} else {
				item[f.key] = value(0)
			}
		}
		items = append(items, item)
	}
	return map[string]any{"items": items}, leaves
}

type propCase struct {
	out    []byte
	parsed map[string]any
	leaves []leaf
}

func genCase(t *rapid.T) propCase {
	enums := rapid.SliceOfNDistinct(enumValue, 1, 4, rapid.ID[string]).Draw(t, "enums")
	fields := genRules(t, enums)
	doc, leaves := genDocument(t, fields, enums)
	in, _ := json.Marshal(doc)
	out, err := Anonymise(propRules(fields), in)
	if err != nil {
		t.Fatalf("a document within its own rules was refused: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	return propCase{out, parsed, leaves}
}

func (c propCase) published(l leaf) any {
	v := c.parsed["items"].([]any)[l.item].(map[string]any)[l.field.key]
	if l.field.array {
		return v.([]any)[l.index]
	}
	return v
}

func TestPropNoRawValueOutsideTheKeepListIsPublished(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := genCase(t)
		lower := strings.ToLower(string(c.out))
		for _, l := range c.leaves {
			s, ok := l.raw.(string)
			if !ok || l.field.rule.Class == Enum || l.field.rule.Class == Timestamp {
				continue
			}
			if strings.Contains(lower, strings.ToLower(s)) {
				t.Fatalf("%s (%s) was published:\n%s", s, l.field.rule.kind(), c.out)
			}
		}
	})
}

// Within one document the same raw value under the same kind of rule always
// becomes the same alias, and two different raw values never share one.
func TestPropAliasesAreConsistentAndDistinct(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := genCase(t)
		alias := map[string]string{}
		owner := map[string]string{}
		for _, l := range c.leaves {
			s, ok := l.raw.(string)
			if !ok || (l.field.rule.Class != Counter && l.field.rule.Class != Redact) {
				continue
			}
			kind := l.field.rule.kind()
			got := c.published(l).(string)
			if prev, seen := alias[kind+"\x00"+s]; seen && prev != got {
				t.Fatalf("%s mapped to %s and to %s", s, prev, got)
			}
			alias[kind+"\x00"+s] = got
			if prev, seen := owner[kind+"\x00"+got]; seen && prev != s {
				t.Fatalf("%s and %s both became %s", prev, s, got)
			}
			owner[kind+"\x00"+got] = s
		}
	})
}

// Points depend on how old things are relative to each other and to the poll,
// so the shift must keep every timestamp's order and every gap between two.
func TestPropTimestampOrderAndGapsSurvive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := genCase(t)
		type pair struct{ in, out time.Time }
		var ts []pair
		for _, l := range c.leaves {
			if l.field.rule.Class != Timestamp {
				continue
			}
			in, _ := time.Parse(time.RFC3339, l.raw.(string))
			out, err := time.Parse(time.RFC3339, c.published(l).(string))
			if err != nil {
				t.Fatal(err)
			}
			ts = append(ts, pair{in, out})
		}
		for _, a := range ts {
			for _, b := range ts {
				if a.in.Sub(b.in) != a.out.Sub(b.out) {
					t.Fatalf("gap %v became %v", a.in.Sub(b.in), a.out.Sub(b.out))
				}
			}
		}
	})
}
