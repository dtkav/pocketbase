package search

import (
	"fmt"
	"strings"

	"github.com/ganigeorgiev/fexpr"
	"github.com/spf13/cast"
)

type shortCircuitResult struct {
	passed    bool              // a cheap branch evaluated to true → rule passes
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
func tryShortCircuitOr(data []fexpr.ExprGroup, staticData map[string]any) shortCircuitResult {
	branches := splitTopLevelOrs(data)

	// Phase 1: top-level OR splitting
	if len(branches) > 1 {
		var expensive []fexpr.ExprGroup
		anyOptimized := false

		for _, branch := range branches {
			if isCheapBranch(branch) {
				if evaluateCheapBranch(branch, staticData) {
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
	return simplifyNestedOrs(data, staticData)
}

// simplifyNestedOrs walks an AND chain of expression groups and recursively
// simplifies any nested parenthesized groups that contain OR branches.
//
// When a nested group simplifies to true, it is removed from the AND chain
// (true AND X = X). When it simplifies to false, the entire chain is false
// (false AND X = false).
func simplifyNestedOrs(data []fexpr.ExprGroup, staticData map[string]any) shortCircuitResult {
	result := make([]fexpr.ExprGroup, 0, len(data))
	anyModified := false

	for _, group := range data {
		inner, ok := group.Item.([]fexpr.ExprGroup)
		if !ok {
			result = append(result, group)
			continue
		}

		innerResult := tryShortCircuitOr(inner, staticData)

		switch {
		case innerResult.passed:
			// group is always true → skip (AND chain: no-op)
			anyModified = true
		case innerResult.remaining != nil && len(innerResult.remaining) == 0:
			// group is always false → entire AND chain is false
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

// isCheapBranch returns true if every identifier in the branch starts with
// @request. (meaning it can be resolved from in-memory static data without SQL).
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
		return strings.HasPrefix(t.Literal, "@request.")
	case fexpr.TokenText, fexpr.TokenNumber:
		return true
	default:
		return false // functions, etc.
	}
}

// evaluateCheapBranch evaluates a branch of ExprGroups against static data.
// All identifiers must be @request.* fields. Returns false for any
// unsupported construct (conservative — never incorrectly short-circuits).
func evaluateCheapBranch(branch []fexpr.ExprGroup, staticData map[string]any) bool {
	result := true

	for i, group := range branch {
		val := evalGroup(group, staticData)

		if i == 0 {
			result = val
		} else if group.Join == fexpr.JoinOr {
			result = result || val
		} else {
			result = result && val
		}
	}

	return result
}

func evalGroup(group fexpr.ExprGroup, staticData map[string]any) bool {
	switch item := group.Item.(type) {
	case fexpr.Expr:
		return evalExpr(item, staticData)
	case fexpr.ExprGroup:
		return evalGroup(item, staticData)
	case []fexpr.ExprGroup:
		return evaluateCheapBranch(item, staticData)
	default:
		return false
	}
}

func evalExpr(expr fexpr.Expr, staticData map[string]any) bool {
	left := resolveStaticToken(expr.Left, staticData)
	right := resolveStaticToken(expr.Right, staticData)

	leftStr := fmt.Sprint(left)
	rightStr := fmt.Sprint(right)

	switch expr.Op {
	case fexpr.SignEq, fexpr.SignAnyEq:
		return leftStr == rightStr
	case fexpr.SignNeq, fexpr.SignAnyNeq:
		return leftStr != rightStr
	case fexpr.SignLike, fexpr.SignAnyLike:
		return strings.Contains(leftStr, rightStr)
	case fexpr.SignNlike, fexpr.SignAnyNlike:
		return !strings.Contains(leftStr, rightStr)
	case fexpr.SignLt, fexpr.SignAnyLt:
		lf, rf, ok := toFloats(leftStr, rightStr)
		return ok && lf < rf
	case fexpr.SignLte, fexpr.SignAnyLte:
		lf, rf, ok := toFloats(leftStr, rightStr)
		return ok && lf <= rf
	case fexpr.SignGt, fexpr.SignAnyGt:
		lf, rf, ok := toFloats(leftStr, rightStr)
		return ok && lf > rf
	case fexpr.SignGte, fexpr.SignAnyGte:
		lf, rf, ok := toFloats(leftStr, rightStr)
		return ok && lf >= rf
	default:
		return false
	}
}

// resolveStaticToken resolves a token to its Go value.
// For identifiers, walks the static data map.
// For text/number literals, returns the literal value.
func resolveStaticToken(t fexpr.Token, staticData map[string]any) any {
	switch t.Type {
	case fexpr.TokenIdentifier:
		return resolveStaticIdentifier(t.Literal, staticData)
	case fexpr.TokenText:
		return t.Literal
	case fexpr.TokenNumber:
		return t.Literal
	default:
		return nil
	}
}

// resolveStaticIdentifier resolves @request.auth.scopes → staticData["auth"]["scopes"]
func resolveStaticIdentifier(field string, staticData map[string]any) any {
	// strip "@request." prefix
	field = strings.TrimPrefix(field, "@request.")

	parts := strings.Split(field, ".")
	var current any = staticData

	for _, part := range parts {
		switch m := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = m[part]
			if !ok {
				return nil
			}
		case map[string]string:
			v, ok := m[part]
			if !ok {
				return nil
			}
			return v
		default:
			return nil
		}
	}

	return current
}

func toFloats(a, b string) (float64, float64, bool) {
	af, err := cast.ToFloat64E(a)
	if err != nil {
		return 0, 0, false
	}
	bf, err := cast.ToFloat64E(b)
	if err != nil {
		return 0, 0, false
	}
	return af, bf, true
}

// CheapBranch represents an OR branch that can be resolved from in-memory
// @request.* fields without hitting the database.
type CheapBranch struct {
	Expression string `json:"expression"`
}

// AnalyzeCheapBranches parses a filter rule and returns the list of OR branches
// that only reference @request.* fields (cheap branches that short-circuit).
func AnalyzeCheapBranches(raw string) ([]CheapBranch, error) {
	if raw == "" {
		return nil, nil
	}

	data, err := fexpr.Parse(raw)
	if err != nil {
		return nil, err
	}

	var result []CheapBranch

	// Check top-level OR branches
	branches := splitTopLevelOrs(data)
	if len(branches) > 1 {
		for _, branch := range branches {
			if isCheapBranch(branch) {
				result = append(result, CheapBranch{
					Expression: branchToString(branch),
				})
			}
		}
	}

	// Recurse into nested groups (AND chains with parenthesized ORs)
	for _, group := range data {
		result = append(result, findCheapBranchesInGroup(group)...)
	}

	return result, nil
}

// findCheapBranchesInGroup recurses into nested parenthesized groups to find
// cheap OR branches within them.
func findCheapBranchesInGroup(group fexpr.ExprGroup) []CheapBranch {
	inner, ok := group.Item.([]fexpr.ExprGroup)
	if !ok {
		return nil
	}

	var result []CheapBranch

	branches := splitTopLevelOrs(inner)
	if len(branches) > 1 {
		for _, branch := range branches {
			if isCheapBranch(branch) {
				result = append(result, CheapBranch{
					Expression: branchToString(branch),
				})
			}
		}
	}

	// Continue recursing
	for _, g := range inner {
		result = append(result, findCheapBranchesInGroup(g)...)
	}

	return result
}

// branchToString reconstructs a human-readable expression string from parsed
// ExprGroup tokens.
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

// StripCheapBranches removes all cheap (in-memory @request.*) OR branches
// from a rule and returns only the expensive remainder as a filter string.
// This produces the worst-case rule — the path that actually hits the database.
//
// Returns empty string if the entire rule is cheap (no expensive branches).
func StripCheapBranches(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	data, err := fexpr.Parse(raw)
	if err != nil {
		return "", err
	}

	stripped := stripCheapFromGroups(data)
	if stripped == nil || len(stripped) == 0 {
		return "", nil
	}

	return branchToString(stripped), nil
}

// stripCheapFromGroups removes cheap OR branches at the current level
// and recurses into nested groups.
func stripCheapFromGroups(data []fexpr.ExprGroup) []fexpr.ExprGroup {
	branches := splitTopLevelOrs(data)

	if len(branches) > 1 {
		// Filter out cheap branches, keep only expensive ones
		var expensive []fexpr.ExprGroup
		for _, branch := range branches {
			if !isCheapBranch(branch) {
				if len(expensive) == 0 && len(branch) > 0 {
					// Fix join operator on first remaining branch
					fixed := make([]fexpr.ExprGroup, len(branch))
					copy(fixed, branch)
					fixed[0].Join = fexpr.JoinAnd
					expensive = append(expensive, fixed...)
				} else {
					expensive = append(expensive, branch...)
				}
			}
		}

		if len(expensive) == 0 {
			return nil
		}

		// Recurse into the remaining expensive groups
		return stripCheapFromNestedGroups(expensive)
	}

	// No top-level ORs — recurse into nested groups
	return stripCheapFromNestedGroups(data)
}

// stripCheapFromNestedGroups walks an expression list and recurses into
// any parenthesized groups to strip their cheap branches.
func stripCheapFromNestedGroups(data []fexpr.ExprGroup) []fexpr.ExprGroup {
	result := make([]fexpr.ExprGroup, 0, len(data))

	for _, group := range data {
		inner, ok := group.Item.([]fexpr.ExprGroup)
		if !ok {
			result = append(result, group)
			continue
		}

		stripped := stripCheapFromGroups(inner)
		if stripped == nil || len(stripped) == 0 {
			// Entire nested group was cheap — skip it from the AND chain
			continue
		}

		newGroup := group
		newGroup.Item = stripped
		result = append(result, newGroup)
	}

	return result
}
