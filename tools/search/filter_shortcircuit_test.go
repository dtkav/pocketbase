package search

import (
	"strings"
	"testing"

	"github.com/ganigeorgiev/fexpr"
)

type testStaticResolver map[string]bool

func (r testStaticResolver) EvaluateStaticExpr(data []fexpr.ExprGroup) (bool, bool, error) {
	result, ok := r[branchToString(data)]
	return result, ok, nil
}

func (r testStaticResolver) ValidateStaticExpr(data []fexpr.ExprGroup, maxExpressions int) error {
	return nil
}

var (
	botStaticResolver = testStaticResolver{
		`@request.auth.collectionName = "bots"`:                                      true,
		`@request.auth.collectionName = "admin"`:                                     false,
		`@request.auth.collectionName = "users"`:                                     false,
		`@request.method = "DELETE"`:                                                 false,
		`@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays"`:   true,
		`(@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays")`: true,
		`(@request.auth.collectionName = "bots" && @request.auth.scopes ~ "users")`:  true,
	}

	humanStaticResolver = testStaticResolver{
		`@request.auth.collectionName = "bots"`:                                      false,
		`@request.auth.collectionName = "admin"`:                                     false,
		`@request.auth.collectionName = "users"`:                                     true,
		`@request.method = "DELETE"`:                                                 false,
		`@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays"`:   false,
		`(@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays")`: false,
		`(@request.auth.collectionName = "bots" && @request.auth.scopes ~ "users")`:  false,
	}

	noAuthStaticResolver = testStaticResolver{
		`(@request.auth.id != "" && @request.auth.collectionName != "users")`: false,
	}
)

func branchToString(branch []fexpr.ExprGroup) string {
	var parts []string
	for i, group := range branch {
		if i > 0 {
			if group.Join == fexpr.JoinOr {
				parts = append(parts, "||")
			} else {
				parts = append(parts, "&&")
			}
		}
		parts = append(parts, groupToString(group))
	}
	return strings.Join(parts, " ")
}

func groupToString(group fexpr.ExprGroup) string {
	switch item := group.Item.(type) {
	case fexpr.Expr:
		return exprToString(item)
	case fexpr.ExprGroup:
		return groupToString(item)
	case []fexpr.ExprGroup:
		return "(" + branchToString(item) + ")"
	default:
		return "?"
	}
}

func exprToString(expr fexpr.Expr) string {
	left := tokenToString(expr.Left)
	right := tokenToString(expr.Right)
	return left + " " + string(expr.Op) + " " + right
}

func tokenToString(t fexpr.Token) string {
	switch t.Type {
	case fexpr.TokenText:
		return `"` + t.Literal + `"`
	default:
		return t.Literal
	}
}

func TestSplitTopLevelOrs(t *testing.T) {
	scenarios := []struct {
		name          string
		filter        string
		expectedCount int
		expectedLens  []int // length of each branch
	}{
		{
			"empty",
			"",
			0,
			nil,
		},
		{
			"single expression (no OR)",
			"a = 1",
			1,
			[]int{1},
		},
		{
			"two AND expressions (no OR)",
			"a = 1 && b = 2",
			1,
			[]int{2},
		},
		{
			"two OR branches",
			"a = 1 || b = 2",
			2,
			[]int{1, 1},
		},
		{
			"three OR branches",
			"a = 1 || b = 2 || c = 3",
			3,
			[]int{1, 1, 1},
		},
		{
			"mixed AND and OR",
			"a = 1 && b = 2 || c = 3 && d = 4",
			2,
			[]int{2, 2},
		},
		{
			"OR with grouped expressions",
			"(a = 1 && b = 2) || c = 3",
			2,
			[]int{1, 1},
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			if s.filter == "" {
				result := splitTopLevelOrs(nil)
				if len(result) != s.expectedCount {
					t.Fatalf("expected %d branches, got %d", s.expectedCount, len(result))
				}
				return
			}

			data, err := fexpr.Parse(s.filter)
			if err != nil {
				t.Fatalf("failed to parse filter %q: %v", s.filter, err)
			}

			result := splitTopLevelOrs(data)
			if len(result) != s.expectedCount {
				t.Fatalf("expected %d branches, got %d", s.expectedCount, len(result))
			}

			if s.expectedLens != nil {
				for i, expectedLen := range s.expectedLens {
					if len(result[i]) != expectedLen {
						t.Errorf("branch %d: expected %d groups, got %d", i, expectedLen, len(result[i]))
					}
				}
			}
		})
	}
}

func TestIsCheapBranch(t *testing.T) {
	scenarios := []struct {
		name     string
		filter   string
		branchN  int // which OR branch to test (0-indexed)
		expected bool
	}{
		{
			"@request.auth field = text",
			`@request.auth.collectionName = "bots"`,
			0,
			true,
		},
		{
			"@request.context field",
			`@request.context = "default"`,
			0,
			true,
		},
		{
			"@request.method field",
			`@request.method = "GET"`,
			0,
			true,
		},
		{
			"@request.auth with LIKE",
			`@request.auth.scopes ~ "relays"`,
			0,
			true,
		},
		{
			"two @request fields ANDed",
			`@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays"`,
			0,
			true,
		},
		{
			"@collection reference (expensive)",
			`@collection.relay_roles.user = @request.auth.id`,
			0,
			false,
		},
		{
			"plain record field (expensive)",
			`status = "active"`,
			0,
			false,
		},
		{
			"mixed: cheap OR branch first, expensive second",
			`@request.auth.collectionName = "bots" || @collection.relay_roles.user = @request.auth.id`,
			0,
			true,
		},
		{
			"mixed: expensive OR branch",
			`@request.auth.collectionName = "bots" || @collection.relay_roles.user = @request.auth.id`,
			1,
			false,
		},
		{
			"number literal on right side",
			`@request.auth.verified = 1`,
			0,
			true,
		},
		{
			"static function args",
			`strftime("%Y", @request.body.date) = "2024"`,
			0,
			true,
		},
		{
			"function with record field arg",
			`strftime("%Y", created) = "2024"`,
			0,
			false,
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			data, err := fexpr.Parse(s.filter)
			if err != nil {
				t.Fatalf("failed to parse filter %q: %v", s.filter, err)
			}

			branches := splitTopLevelOrs(data)
			if s.branchN >= len(branches) {
				t.Fatalf("branch %d out of range (have %d branches)", s.branchN, len(branches))
			}

			result := isCheapBranch(branches[s.branchN])
			if result != s.expected {
				t.Fatalf("expected isCheapBranch=%v, got %v", s.expected, result)
			}
		})
	}
}

func TestTryShortCircuitOr(t *testing.T) {
	scenarios := []struct {
		name              string
		filter            string
		resolver          testStaticResolver
		expectPassed      bool
		expectNilRemain   bool // remaining == nil (no optimization)
		expectRemainCount int  // len(remaining) when not nil
	}{
		{
			"no OR - no optimization",
			`@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays"`,
			botStaticResolver,
			false,
			true,
			0,
		},
		{
			"bot matches cheap branch - passed",
			`@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays" || @collection.relay_roles.user = @request.auth.id`,
			botStaticResolver,
			true,
			false,
			0,
		},
		{
			"human fails cheap branch - only expensive remains",
			`@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays" || @collection.relay_roles.user = @request.auth.id`,
			humanStaticResolver,
			false,
			false,
			1, // just the expensive branch with one ExprGroup
		},
		{
			"no cheap branches - no optimization",
			`@collection.relay_roles.user = @request.auth.id || status = "active"`,
			botStaticResolver,
			false,
			true,
			0,
		},
		{
			"all cheap branches, none match - empty remaining",
			`@request.auth.collectionName = "bots" || @request.auth.collectionName = "admin"`,
			humanStaticResolver,
			false,
			false,
			0,
		},
		{
			"all cheap, first matches - passed",
			`@request.auth.collectionName = "bots" || @request.auth.collectionName = "users"`,
			botStaticResolver,
			true,
			false,
			0,
		},
		{
			"three branches: cheap-match, cheap-miss, expensive",
			`@request.auth.collectionName = "bots" || @request.method = "DELETE" || @collection.relay_roles.user = @request.auth.id`,
			botStaticResolver,
			true,
			false,
			0,
		},
		{
			"three branches: cheap-miss, expensive, cheap-miss",
			`@request.auth.collectionName = "admin" || @collection.relay_roles.user = @request.auth.id || @request.method = "DELETE"`,
			humanStaticResolver,
			false,
			false,
			1, // just the expensive branch
		},
		{
			"guest fails generated relation rule auth branch",
			`id = "" || (@request.auth.id != "" && @request.auth.collectionName != "users")`,
			noAuthStaticResolver,
			false,
			false,
			1, // just the always-empty id branch
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			data, err := fexpr.Parse(s.filter)
			if err != nil {
				t.Fatalf("failed to parse filter %q: %v", s.filter, err)
			}

			result := tryShortCircuitOr(data, s.resolver)

			if result.passed != s.expectPassed {
				t.Fatalf("expected passed=%v, got %v", s.expectPassed, result.passed)
			}

			if s.expectNilRemain {
				if result.remaining != nil {
					t.Fatalf("expected remaining to be nil, got %v", result.remaining)
				}
			} else if !s.expectPassed {
				if result.remaining == nil {
					t.Fatal("expected remaining to be non-nil")
				}
				if len(result.remaining) != s.expectRemainCount {
					t.Fatalf("expected %d remaining groups, got %d", s.expectRemainCount, len(result.remaining))
				}
			}
		})
	}
}

func TestTryShortCircuitOrRemainingJoinOp(t *testing.T) {
	// When the first branch is cheap and doesn't match, the second (expensive)
	// branch should have its first ExprGroup's Join reset from OR to AND so
	// it doesn't create an invalid SQL expression.
	filter := `@request.auth.collectionName = "bots" || @collection.relay_roles.user = @request.auth.id`

	data, err := fexpr.Parse(filter)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	result := tryShortCircuitOr(data, humanStaticResolver)
	if result.passed {
		t.Fatal("expected passed=false")
	}
	if result.remaining == nil || len(result.remaining) == 0 {
		t.Fatal("expected non-empty remaining")
	}

	// The first remaining group should have JoinAnd, not JoinOr
	if result.remaining[0].Join != fexpr.JoinAnd {
		t.Fatalf("expected first remaining group to have JoinAnd, got %q", result.remaining[0].Join)
	}
}

func TestTryShortCircuitOrNested(t *testing.T) {
	scenarios := []struct {
		name              string
		filter            string
		resolver          testStaticResolver
		expectPassed      bool
		expectNilRemain   bool
		expectRemainCount int
	}{
		{
			// A && (cheap_true || expensive) -> remaining = [A]
			"nested: bot matches cheap branch inside group",
			`@request.auth.verified = true && ((@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays") || @collection.relay_roles.user = @request.auth.id)`,
			botStaticResolver,
			false,
			false,
			1, // just the @request.auth.verified expression
		},
		{
			// A && (cheap_false || expensive) -> remaining = [A, (expensive)]
			"nested: human fails cheap branch, expensive remains",
			`@request.auth.verified = true && ((@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays") || @collection.relay_roles.user = @request.auth.id)`,
			humanStaticResolver,
			false,
			false,
			2, // verified expression + simplified group
		},
		{
			// A && (cheap_true || cheap_false) -> remaining = [A]
			"nested: all cheap, one matches - group true, remove from AND chain",
			`@request.auth.verified = true && (@request.auth.collectionName = "bots" || @request.auth.collectionName = "admin")`,
			botStaticResolver,
			false,
			false,
			1, // just verified
		},
		{
			// A && (cheap_false || cheap_false) -> false
			"nested: all cheap, none match - AND chain is false",
			`@request.auth.verified = true && (@request.auth.collectionName = "bots" || @request.auth.collectionName = "admin")`,
			humanStaticResolver,
			false,
			false,
			0, // empty remaining -> false
		},
		{
			// (cheap_true || expensive) - only group, no other top-level terms
			"nested: only group, cheap matches - passed",
			`((@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays") || @collection.relay_roles.user = @request.auth.id)`,
			botStaticResolver,
			true,
			false,
			0,
		},
		{
			"nested: no cheap branches in group - no optimization",
			`@request.auth.verified = true && (@collection.relay_roles.user = @request.auth.id || creator = @request.auth.id)`,
			botStaticResolver,
			false,
			true,
			0,
		},
		{
			// Real relays viewRule pattern: A && (expensive || record_field || cheap)
			"nested: relays viewRule with bot - bot matches",
			`@request.auth.id != "" && ((@collection.relay_roles:rr.user ?= @request.auth.id && @collection.relay_roles:rr.relay ?= id) || creator = @request.auth.id || (@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays"))`,
			botStaticResolver,
			false,
			false,
			1, // just auth.id != ""
		},
		{
			// Real relays viewRule pattern: human -> remove cheap branch, keep expensive + record
			"nested: relays viewRule with bot - human",
			`@request.auth.id != "" && ((@collection.relay_roles:rr.user ?= @request.auth.id && @collection.relay_roles:rr.relay ?= id) || creator = @request.auth.id || (@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays"))`,
			humanStaticResolver,
			false,
			false,
			2, // auth.id != "" + simplified group (expensive + record branches only)
		},
		{
			// Real users viewRule pattern
			"nested: users viewRule - bot matches",
			`@request.auth.verified = true && ((@request.auth.collectionName = "users" && @collection.collaborations.auth_user ?= @request.auth.id && @collection.collaborations.user ?= id) || (@request.auth.collectionName = "bots" && @request.auth.scopes ~ "users"))`,
			botStaticResolver,
			false,
			false,
			1, // just verified
		},
		{
			// Users viewRule with human: bot branch removed, users+collab branch remains
			"nested: users viewRule - human",
			`@request.auth.verified = true && ((@request.auth.collectionName = "users" && @collection.collaborations.auth_user ?= @request.auth.id && @collection.collaborations.user ?= id) || (@request.auth.collectionName = "bots" && @request.auth.scopes ~ "users"))`,
			humanStaticResolver,
			false,
			false,
			2, // verified + simplified group
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			data, err := fexpr.Parse(s.filter)
			if err != nil {
				t.Fatalf("failed to parse filter %q: %v", s.filter, err)
			}

			result := tryShortCircuitOr(data, s.resolver)

			if result.passed != s.expectPassed {
				t.Fatalf("expected passed=%v, got %v", s.expectPassed, result.passed)
			}

			if s.expectNilRemain {
				if result.remaining != nil {
					t.Fatalf("expected remaining to be nil, got %v", result.remaining)
				}
			} else if !s.expectPassed {
				if result.remaining == nil {
					t.Fatal("expected remaining to be non-nil")
				}
				if len(result.remaining) != s.expectRemainCount {
					t.Fatalf("expected %d remaining groups, got %d", s.expectRemainCount, len(result.remaining))
				}
			}
		})
	}
}

func TestBranchToString(t *testing.T) {
	scenarios := []struct {
		name     string
		filter   string
		branchN  int
		expected string
	}{
		{
			"simple equality",
			`@request.auth.id != ""`,
			0,
			`@request.auth.id != ""`,
		},
		{
			"AND chain",
			`@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays"`,
			0,
			`@request.auth.collectionName = "bots" && @request.auth.scopes ~ "relays"`,
		},
		{
			"first OR branch",
			`@request.auth.collectionName = "bots" || status = "active"`,
			0,
			`@request.auth.collectionName = "bots"`,
		},
		{
			"number literal",
			`@request.auth.verified = 1`,
			0,
			`@request.auth.verified = 1`,
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			data, err := fexpr.Parse(s.filter)
			if err != nil {
				t.Fatalf("failed to parse filter %q: %v", s.filter, err)
			}

			branches := splitTopLevelOrs(data)
			if s.branchN >= len(branches) {
				t.Fatalf("branch %d out of range (have %d branches)", s.branchN, len(branches))
			}

			result := branchToString(branches[s.branchN])
			if result != s.expected {
				t.Fatalf("expected %q, got %q", s.expected, result)
			}
		})
	}
}
