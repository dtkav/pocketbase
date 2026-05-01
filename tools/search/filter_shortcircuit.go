package search

import (
	"strings"

	"github.com/ganigeorgiev/fexpr"
)

type shortCircuitResult struct {
	passed    bool              // a cheap branch evaluated to true; rule passes
	remaining []fexpr.ExprGroup // expensive branches only (nil = no optimization possible)
}

// tryShortCircuitOr attempts to evaluate cheap OR branches that only reference
// @request.* fields against the provided static data.
//
// It operates in two phases:
//
// Phase 1 (top-level ORs): splits the expression at top-level OR boundaries
// and evaluates cheap branches. If any cheap branch matches, the entire rule
// passes. If some don't match, they are excluded (removing the poisonous OR).
//
// Phase 2 (nested groups): when the top level is a pure AND chain, recurses
// into nested parenthesized groups to simplify OR branches within them.
// This handles the common rule pattern: A && (cheap || expensive).
func tryShortCircuitOr(data []fexpr.ExprGroup, evaluator StaticResolver) shortCircuitResult {
	branches := splitTopLevelOrs(data)

	// Phase 1: top-level OR splitting
	if len(branches) > 1 {
		var expensive []fexpr.ExprGroup
		anyOptimized := false

		for _, branch := range branches {
			if ok, matched := evaluateStaticBranch(branch, evaluator); ok {
				if matched {
					return shortCircuitResult{passed: true}
				}
				anyOptimized = true
			} else {
				if anyOptimized && len(expensive) == 0 && len(branch) > 0 {
					fixed := make([]fexpr.ExprGroup, len(branch))
					copy(fixed, branch)
					fixed[0].Join = fexpr.JoinAnd
					expensive = append(expensive, fixed...)
				} else {
					expensive = append(expensive, branch...)
				}
			}
		}

		if anyOptimized {
			if expensive == nil {
				expensive = []fexpr.ExprGroup{}
			}
			return shortCircuitResult{remaining: expensive}
		}

		return shortCircuitResult{}
	}

	// Phase 2: no top-level ORs (pure AND chain at this level).
	// Recurse into nested groups to simplify their internal OR branches.
	return simplifyNestedOrs(data, evaluator)
}

// simplifyNestedOrs walks an AND chain of expression groups and recursively
// simplifies any nested parenthesized groups that contain OR branches.
//
// When a nested group simplifies to true, it is removed from the AND chain
// (true AND X = X). When it simplifies to false, the entire chain is false
// (false AND X = false).
func simplifyNestedOrs(data []fexpr.ExprGroup, evaluator StaticResolver) shortCircuitResult {
	result := make([]fexpr.ExprGroup, 0, len(data))
	anyModified := false

	for _, group := range data {
		inner, ok := group.Item.([]fexpr.ExprGroup)
		if !ok {
			result = append(result, group)
			continue
		}

		innerResult := tryShortCircuitOr(inner, evaluator)

		switch {
		case innerResult.passed:
			// group is always true - skip (AND chain: no-op)
			anyModified = true
		case innerResult.remaining != nil && len(innerResult.remaining) == 0:
			// group is always false - entire AND chain is false
			return shortCircuitResult{remaining: []fexpr.ExprGroup{}}
		case innerResult.remaining != nil:
			anyModified = true
			newGroup := group
			newGroup.Item = innerResult.remaining
			result = append(result, newGroup)
		default:
			result = append(result, group)
		}
	}

	if !anyModified {
		return shortCircuitResult{}
	}

	if len(result) == 0 {
		return shortCircuitResult{passed: true}
	}

	return shortCircuitResult{remaining: result}
}

// splitTopLevelOrs splits parsed expression groups at top-level OR boundaries.
//
// Given `A && B || C && D` parsed as [{A, &&}, {B, &&}, {C, ||}, {D, &&}],
// this returns [[{A, &&}, {B, &&}], [{C, ||}, {D, &&}]].
func splitTopLevelOrs(data []fexpr.ExprGroup) [][]fexpr.ExprGroup {
	if len(data) == 0 {
		return nil
	}

	var branches [][]fexpr.ExprGroup
	var current []fexpr.ExprGroup

	for _, group := range data {
		if group.Join == fexpr.JoinOr && len(current) > 0 {
			branches = append(branches, current)
			current = []fexpr.ExprGroup{group}
		} else {
			current = append(current, group)
		}
	}

	if len(current) > 0 {
		branches = append(branches, current)
	}

	return branches
}

// isCheapBranch returns true if every token in the branch could represent a
// static request expression. The StaticResolver still makes the final call by
// compiling the branch and rejecting record-dependent SQL.
func isCheapBranch(branch []fexpr.ExprGroup) bool {
	for _, group := range branch {
		if !isCheapGroup(group) {
			return false
		}
	}
	return true
}

func isCheapGroup(group fexpr.ExprGroup) bool {
	switch item := group.Item.(type) {
	case fexpr.Expr:
		return isCheapToken(item.Left) && isCheapToken(item.Right)
	case fexpr.ExprGroup:
		return isCheapGroup(item)
	case []fexpr.ExprGroup:
		for _, g := range item {
			if !isCheapGroup(g) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func isCheapToken(t fexpr.Token) bool {
	switch t.Type {
	case fexpr.TokenIdentifier:
		return strings.HasPrefix(t.Literal, "@request.") || isNormalizedStaticIdentifier(t.Literal)
	case fexpr.TokenText, fexpr.TokenNumber:
		return true
	case fexpr.TokenFunction:
		if _, ok := TokenFunctions[t.Literal]; !ok {
			return false
		}

		args, ok := t.Meta.([]fexpr.Token)
		if !ok {
			return false
		}

		for _, arg := range args {
			if !isCheapToken(arg) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func isNormalizedStaticIdentifier(identifier string) bool {
	_, ok := normalizedIdentifiers[strings.ToLower(identifier)]
	return ok
}

func evaluateStaticBranch(branch []fexpr.ExprGroup, evaluator StaticResolver) (ok bool, matched bool) {
	if !isCheapBranch(branch) {
		return false, false
	}

	matched, ok, err := evaluator.EvaluateStaticExpr(branch)
	if err != nil {
		return false, false
	}

	return ok, matched
}
