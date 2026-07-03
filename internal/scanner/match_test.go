package scanner

import (
	"sort"
	"testing"

	"github.com/jgsqware/pine/internal/model"
)

// testInventory builds a small inventory:
//
//	web1, web2      → groups: [webservers, prod]
//	web3            → groups: [webservers, staging]
//	db1             → groups: [databases, prod]
//	db2             → groups: [databases, staging]
func testInventory() *model.Inventory {
	return &model.Inventory{
		Hosts: []model.Host{
			{Name: "web1", Groups: []string{"webservers", "prod"}},
			{Name: "web2", Groups: []string{"webservers", "prod"}},
			{Name: "web3", Groups: []string{"webservers", "staging"}},
			{Name: "db1", Groups: []string{"databases", "prod"}},
			{Name: "db2", Groups: []string{"databases", "staging"}},
		},
	}
}

func sortedHosts(h []string) []string {
	out := make([]string, len(h))
	copy(out, h)
	sort.Strings(out)
	return out
}

func TestMatchHosts_UnionBasic(t *testing.T) {
	inv := testInventory()
	got := sortedHosts(MatchHosts("webservers", inv))
	want := []string{"web1", "web2", "web3"}
	if len(got) != len(want) {
		t.Fatalf("webservers: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("webservers[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMatchHosts_ExclusionNotStaging(t *testing.T) {
	// "webservers:!staging" → webservers minus staging members
	inv := testInventory()
	got := sortedHosts(MatchHosts("webservers:!staging", inv))
	// web3 is in staging → must be excluded
	for _, h := range got {
		if h == "web3" {
			t.Errorf("web3 (staging) must be excluded but appeared in %v", got)
		}
	}
	// web1 and web2 (prod) must still be present
	present := map[string]bool{}
	for _, h := range got {
		present[h] = true
	}
	for _, want := range []string{"web1", "web2"} {
		if !present[want] {
			t.Errorf("%s should be in result but is missing from %v", want, got)
		}
	}
}

func TestMatchHosts_IntersectionWithProd(t *testing.T) {
	// "webservers:&prod" → webservers ∩ prod = web1, web2
	inv := testInventory()
	got := sortedHosts(MatchHosts("webservers:&prod", inv))
	want := []string{"web1", "web2"}
	if len(got) != len(want) {
		t.Fatalf("webservers:&prod: got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] got %q want %q", i, got[i], w)
		}
	}
}

func TestMatchHosts_CombinedUnionIntersectionExclusion(t *testing.T) {
	// "webservers:databases:&prod:!web1"
	//   union(webservers ∪ databases) = {web1,web2,web3,db1,db2}
	//   ∩ prod                        = {web1,web2,db1}
	//   ! web1 (host name)            = {web2,db1}
	inv := testInventory()
	got := sortedHosts(MatchHosts("webservers:databases:&prod:!web1", inv))
	want := []string{"db1", "web2"}
	if len(got) != len(want) {
		t.Fatalf("combined: got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] got %q want %q", i, got[i], w)
		}
	}
}

func TestMatchHosts_AllWithExclusion(t *testing.T) {
	// "all:!staging" → all hosts minus staging group
	inv := testInventory()
	got := sortedHosts(MatchHosts("all:!staging", inv))
	// staging members: web3, db2 must be absent
	for _, h := range got {
		if h == "web3" || h == "db2" {
			t.Errorf("staging host %q must be excluded but is in %v", h, got)
		}
	}
	// prod members: web1, web2, db1 must be present
	present := map[string]bool{}
	for _, h := range got {
		present[h] = true
	}
	for _, want := range []string{"web1", "web2", "db1"} {
		if !present[want] {
			t.Errorf("%s should be in result after excluding staging, got %v", want, got)
		}
	}
}

func TestMatchHosts_NonExistentGroup(t *testing.T) {
	// A group that doesn't exist → no hosts matched; union has a term so
	// hasUnion=true, but the expandTerm call returns empty.
	inv := testInventory()
	got := MatchHosts("nonexistent_group", inv)
	if len(got) != 0 {
		t.Errorf("nonexistent_group: expected empty, got %v", got)
	}
}

func TestMatchHosts_OnlyExclusion_ReturnsNil(t *testing.T) {
	// "!staging" with no union term → no union, result is nil
	inv := testInventory()
	got := MatchHosts("!staging", inv)
	if len(got) != 0 {
		t.Errorf("only exclusion with no union: expected nil/empty, got %v", got)
	}
}

func TestMatchHosts_CommaUnion(t *testing.T) {
	// Comma is treated the same as colon for union.
	inv := testInventory()
	got := sortedHosts(MatchHosts("web1,web2", inv))
	want := []string{"web1", "web2"}
	if len(got) != len(want) {
		t.Fatalf("comma union: got %v, want %v", got, want)
	}
}

func TestHasUnsupportedPattern_Regex(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{"webservers", false},
		{"webservers:!staging", false},
		{"webservers:&prod", false},
		{"~web.*", true},
		{"webservers:~staging", true},
		{"!~web.*", true},
		{"webservers[0:5]", true},
		{"webservers:&prod[0]", true},
		{"all:!staging", false},
	}
	for _, tc := range cases {
		got := HasUnsupportedPattern(tc.pattern)
		if got != tc.want {
			t.Errorf("HasUnsupportedPattern(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}
