package smartrouter

import (
	"math"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// cheapestTierInputPrices returns the minimum ordinary-input and cache-read
// prices across every statically identifiable tier() value in exprStr.
// Prices are expressed in USD per million input tokens, matching the billing
// expression contract. Conditions selecting a tier are deliberately not
// evaluated: prediction uses the lowest configured ordinary-input price and
// lowest configured cache-read price independently, while settlement
// continues to evaluate the frozen request expression normally.
//
// The function is intentionally conservative. If the expression cannot be
// parsed, contains no tier() value, or any tier value cannot be evaluated as a
// finite non-negative number, it returns ok=false rather than inventing a
// price. The optional hash is accepted as the caller's revision identity;
// historical revisions are not required to be SHA-256 strings.
func cheapestTierInputPrices(exprStr, expressionHash string) (baseUSD, cacheUSD float64, ok bool) {
	_ = expressionHash
	_, body := parseTierExprVersion(exprStr)
	if body == "" {
		return 0, 0, false
	}
	tree, err := parser.Parse(body)
	if err != nil || tree == nil || tree.Node == nil {
		return 0, 0, false
	}

	const million = float64(1_000_000)
	bodyUsed := tierExprUsedVars(body)
	// Wall-clock and media dimensions outside a tier arm can alter the final
	// numeric price even when the tier value itself is static. Reject them
	// rather than silently extracting a partial price contract.
	for _, variable := range []string{"hour", "minute", "weekday", "month", "day", "img", "img_o", "ai", "ao"} {
		if bodyUsed[variable] {
			return 0, 0, false
		}
	}
	var tierValues []tierInputValue
	ast.Walk(&tree.Node, tierCallCollector{values: &tierValues})
	// Raw expressions without tier() are valid single-tier contracts.
	if len(tierValues) == 0 {
		tierValues = append(tierValues, tierInputValue{expression: body})
	}
	var found bool
	for _, tier := range tierValues {
		used := tierExprUsedVars(tier.expression)
		if !used["p"] && !used["cr"] && !used["cc"] && !used["cc1h"] {
			return 0, 0, false
		}
		// Wall-clock functions are evaluated at settlement time and cannot
		// provide a stable configured price for prediction.
		for _, variable := range []string{"hour", "minute", "weekday", "month", "day"} {
			if used[variable] {
				return 0, 0, false
			}
		}
		baseValues, baseOK := numericPriceVariants(tier.expression, priceVector{p: million})
		if !baseOK || len(baseValues) == 0 {
			return 0, 0, false
		}
		base, okBase := minimumFiniteNonNegative(baseValues)
		if !okBase {
			return 0, 0, false
		}
		cache := base
		if used["cr"] {
			cacheValues, cacheOK := numericPriceVariants(tier.expression, priceVector{cr: million})
			if !cacheOK || len(cacheValues) == 0 {
				return 0, 0, false
			}
			cache, okBase = minimumFiniteNonNegative(cacheValues)
			if !okBase {
				return 0, 0, false
			}
		}
		base /= million
		cache /= million
		if !found || base < baseUSD {
			baseUSD = base
		}
		if !found || cache < cacheUSD {
			cacheUSD = cache
		}
		found = true
	}
	if !found || !finiteNonNegative(baseUSD) || !finiteNonNegative(cacheUSD) {
		return 0, 0, false
	}
	return baseUSD, cacheUSD, true
}

// priceVector is deliberately limited to token variables. Request metadata
// may choose a branch, but it must not provide a numeric price multiplier.
type priceVector struct {
	p, c, cr, cc, cc1h float64
}

// numericPriceVariants evaluates all numeric arms of a billing value without
// evaluating conditions. This makes `p * (header(...) ? 8 : 2)` predictable
// while rejecting `p * param("price")`, which would be an invented price.
func numericPriceVariants(source string, vector priceVector) ([]float64, bool) {
	tree, err := parser.Parse(source)
	if err != nil || tree == nil || tree.Node == nil {
		return nil, false
	}
	return numericNodeVariants(tree.Node, vector)
}

func numericNodeVariants(node ast.Node, vector priceVector) ([]float64, bool) {
	switch n := node.(type) {
	case *ast.IntegerNode:
		return []float64{float64(n.Value)}, true
	case *ast.FloatNode:
		return []float64{n.Value}, true
	case *ast.IdentifierNode:
		switch strings.TrimSpace(n.Value) {
		case "p":
			return []float64{vector.p}, true
		case "c":
			return []float64{vector.c}, true
		case "cr":
			return []float64{vector.cr}, true
		case "cc":
			return []float64{vector.cc}, true
		case "cc1h":
			return []float64{vector.cc1h}, true
		default:
			return nil, false
		}
	case *ast.ConstantNode:
		switch value := n.Value.(type) {
		case int:
			return []float64{float64(value)}, true
		case int64:
			return []float64{float64(value)}, true
		case float64:
			return []float64{value}, true
		default:
			return nil, false
		}
	case *ast.UnaryNode:
		values, ok := numericNodeVariants(n.Node, vector)
		if !ok {
			return nil, false
		}
		switch n.Operator {
		case "+":
			return values, true
		case "-":
			for i := range values {
				values[i] = -values[i]
			}
			return values, true
		default:
			return nil, false
		}
	case *ast.ConditionalNode:
		left, leftOK := numericNodeVariants(n.Exp1, vector)
		right, rightOK := numericNodeVariants(n.Exp2, vector)
		if !leftOK || !rightOK {
			return nil, false
		}
		return append(left, right...), true
	case *ast.BinaryNode:
		left, leftOK := numericNodeVariants(n.Left, vector)
		right, rightOK := numericNodeVariants(n.Right, vector)
		if !leftOK || !rightOK {
			return nil, false
		}
		values := make([]float64, 0, len(left)*len(right))
		for _, l := range left {
			for _, r := range right {
				value, ok := applyNumericOperator(n.Operator, l, r)
				if !ok {
					return nil, false
				}
				values = append(values, value)
			}
		}
		return values, true
	case *ast.CallNode:
		callee, ok := n.Callee.(*ast.IdentifierNode)
		if !ok || len(n.Arguments) == 0 {
			return nil, false
		}
		name := strings.TrimSpace(callee.Value)
		if name == "tier" {
			if len(n.Arguments) != 2 {
				return nil, false
			}
			return numericNodeVariants(n.Arguments[1], vector)
		}
		if name != "abs" && name != "ceil" && name != "floor" && name != "max" && name != "min" {
			return nil, false
		}
		args := make([][]float64, len(n.Arguments))
		for i, argument := range n.Arguments {
			var argOK bool
			args[i], argOK = numericNodeVariants(argument, vector)
			if !argOK || len(args[i]) == 0 {
				return nil, false
			}
		}
		values := []float64{}
		for _, first := range args[0] {
			if len(args) == 1 {
				values = append(values, applyNumericUnary(name, first))
				continue
			}
			for _, second := range args[1] {
				if name == "max" {
					values = append(values, math.Max(first, second))
				} else if name == "min" {
					values = append(values, math.Min(first, second))
				} else {
					return nil, false
				}
			}
		}
		return values, true
	default:
		return nil, false
	}
}

func applyNumericUnary(name string, value float64) float64 {
	switch name {
	case "abs":
		return math.Abs(value)
	case "ceil":
		return math.Ceil(value)
	case "floor":
		return math.Floor(value)
	default:
		return value
	}
}

func applyNumericOperator(operator string, left, right float64) (float64, bool) {
	switch operator {
	case "+":
		return left + right, true
	case "-":
		return left - right, true
	case "*":
		return left * right, true
	case "/":
		if right == 0 {
			return 0, false
		}
		return left / right, true
	case "%":
		if right == 0 {
			return 0, false
		}
		return math.Mod(left, right), true
	default:
		return 0, false
	}
}

func minimumFiniteNonNegative(values []float64) (float64, bool) {
	minimum := 0.0
	found := false
	for _, value := range values {
		if !finiteNonNegative(value) {
			continue
		}
		if !found || value < minimum {
			minimum, found = value, true
		}
	}
	return minimum, found
}

type tierCallCollector struct {
	values *[]tierInputValue
}

type tierInputValue struct {
	expression string
}

func (c tierCallCollector) Visit(node *ast.Node) {
	call, ok := (*node).(*ast.CallNode)
	if !ok || len(call.Arguments) != 2 {
		return
	}
	callee, ok := call.Callee.(*ast.IdentifierNode)
	if !ok || callee.Value != "tier" {
		return
	}
	*c.values = append(*c.values, tierInputValue{expression: call.Arguments[1].String()})
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

var tierCompileEnv = map[string]interface{}{
	"p":       float64(0),
	"c":       float64(0),
	"len":     float64(0),
	"cr":      float64(0),
	"cc":      float64(0),
	"cc1h":    float64(0),
	"img":     float64(0),
	"img_o":   float64(0),
	"ai":      float64(0),
	"ao":      float64(0),
	"tier":    func(string, float64) float64 { return 0 },
	"header":  func(string) string { return "" },
	"param":   func(string) interface{} { return nil },
	"has":     func(interface{}, string) bool { return false },
	"hour":    func(string) int { return 0 },
	"minute":  func(string) int { return 0 },
	"weekday": func(string) int { return 0 },
	"month":   func(string) int { return 0 },
	"day":     func(string) int { return 0 },
	"max":     math.Max,
	"min":     math.Min,
	"abs":     math.Abs,
	"ceil":    math.Ceil,
	"floor":   math.Floor,
}

func parseTierExprVersion(expression string) (int, string) {
	return 1, strings.TrimPrefix(expression, "v1:")
}

func tierExprUsedVars(expression string) map[string]bool {
	_, body := parseTierExprVersion(expression)
	program, err := expr.Compile(body, expr.Env(tierCompileEnv), expr.AsFloat64())
	if err != nil {
		return nil
	}
	variables := make(map[string]bool)
	ast.Find(program.Node(), func(node ast.Node) bool {
		if identifier, ok := node.(*ast.IdentifierNode); ok {
			variables[identifier.Value] = true
		}
		return false
	})
	return variables
}
