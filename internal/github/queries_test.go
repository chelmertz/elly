package github

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Every value that names a person, a PR or a page travels as a GraphQL
// variable, so the query text is the same constant on every poll. A recording
// relies on that: the anonymiser keeps the query text as is and only has to
// map the variables, and a value inlined into the text would be published.
func Test_Queries_CarryValuesAsVariables(t *testing.T) {
	cases := []struct {
		name   string
		q      graphqlQuery
		values []string
		want   map[string]any
	}{
		{
			"involves search", querySearchPrs(qualifierInvolves, "someLogin"), []string{"someLogin"},
			map[string]any{"search": "state:open involves:someLogin type:pr archived:false"},
		},
		{
			"review-requested search", querySearchPrs(qualifierReviewRequested, "someLogin"), []string{"someLogin"},
			map[string]any{"search": "state:open review-requested:someLogin type:pr archived:false"},
		},
		{
			"review thread page", queryReviewThreadsPage("PR_node", "cursorValue"), []string{"PR_node", "cursorValue"},
			map[string]any{"id": "PR_node", "after": "cursorValue"},
		},
		{
			"failing checks, first page", queryFailingChecks("PR_node", ""), []string{"PR_node"},
			map[string]any{"id": "PR_node", "after": nil},
		},
		{
			"failing checks, next page", queryFailingChecks("PR_node", "cursorValue"), []string{"PR_node", "cursorValue"},
			map[string]any{"id": "PR_node", "after": "cursorValue"},
		},
		{
			"mergeability", queryMergeability([]string{"PR_a", "PR_b"}), []string{"PR_a", "PR_b"},
			map[string]any{"ids": []string{"PR_a", "PR_b"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, v := range c.values {
				if strings.Contains(c.q.Query, v) {
					t.Errorf("query text carries %q; it belongs in a variable", v)
				}
			}
			if !slices.Contains(queryConstants, c.q.Query) {
				t.Error("query text is not one of queryConstants")
			}
			if diff := cmp.Diff(c.want, c.q.Variables); diff != "" {
				t.Errorf("variables (-want +got):\n%s", diff)
			}
		})
	}
}
