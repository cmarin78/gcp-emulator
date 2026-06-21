// interpreter.go implements a real interpreter for the basic Workflows
// step syntax (sequential steps, assign, switch conditionals, call),
// replacing the previous no-op execution that always resolved to
// SUCCEEDED with the input argument echoed back unchanged.
//
// Scope: this only interprets the JSON form of a workflow definition.
// Real Workflows source can be written in YAML or JSON -- JSON is a
// fully supported, real form (not an emulator invention), but adding a
// YAML parser would pull in this project's first non-stdlib dependency
// beyond bbolt, which Phase 11 deliberately avoids. A sourceContents that
// doesn't parse as the expected JSON shape (e.g. real-world YAML
// workflows, or any other text) falls back to the previous behavior:
// SUCCEEDED, result = the input argument echoed back -- documented as
// the actual boundary of support, not silently wrong.
//
// Within JSON definitions, the interpreted subset is: sequential step
// execution, assign (ordered variable assignment), switch (ordered
// conditional jumps/returns, with the step's own next/return acting as
// the implicit "else"), an unconditional next (including the special
// "end" target), return (ends the (sub)workflow with a value), and call
// (invokes either the sys.log builtin or another subworkflow defined in
// the same document). Expressions use the "${...}" syntax real Workflows
// uses, evaluated by a small expression evaluator supporting literals,
// variables with dotted property access, arithmetic, comparison, and
// logical operators. Connectors, HTTP calls, and list/map literals or
// indexing inside expressions are out of scope and documented as such.
package workflows

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/cesar/gcp-emulator/internal/activity"
)

// maxSteps guards against an infinite step loop turning an execution into
// a runaway goroutine -- a workflow that exceeds it fails with a clear
// error instead of hanging the request forever.
const maxSteps = 10000

// --- definition model -------------------------------------------------

type subworkflow struct {
	Params []string
	Steps  []step
}

type assignment struct {
	Name string
	Expr json.RawMessage
}

type switchCase struct {
	ConditionExpr string
	HasNext       bool
	Next          string
	HasReturn     bool
	Return        json.RawMessage
}

type step struct {
	Name      string
	Assign    []assignment
	Switch    []switchCase
	HasNext   bool
	Next      string
	HasReturn bool
	Return    json.RawMessage
	Call      string
	Args      map[string]json.RawMessage
	Result    string
}

// --- JSON wire shapes used only during parsing -------------------------

type subworkflowJSON struct {
	Params []string          `json:"params,omitempty"`
	Steps  []json.RawMessage `json:"steps"`
}

type stepBodyJSON struct {
	Assign []map[string]json.RawMessage `json:"assign,omitempty"`
	Switch []switchCaseJSON             `json:"switch,omitempty"`
	Next   *string                      `json:"next,omitempty"`
	Return *json.RawMessage             `json:"return,omitempty"`
	Call   string                       `json:"call,omitempty"`
	Args   map[string]json.RawMessage   `json:"args,omitempty"`
	Result string                       `json:"result,omitempty"`
}

type switchCaseJSON struct {
	Condition string           `json:"condition"`
	Next      *string          `json:"next,omitempty"`
	Return    *json.RawMessage `json:"return,omitempty"`
}

// parseDefinition parses sourceContents as a JSON workflow definition: a
// top-level object whose keys are subworkflow names (conventionally
// "main", plus any others a "call" can target), each holding "params" and
// an ordered "steps" array of single-key {stepName: body} objects.
func parseDefinition(sourceContents string) (map[string]subworkflow, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sourceContents), &root); err != nil {
		return nil, err
	}
	def := make(map[string]subworkflow, len(root))
	for name, raw := range root {
		var swj subworkflowJSON
		if err := json.Unmarshal(raw, &swj); err != nil {
			return nil, fmt.Errorf("subworkflow %q: %w", name, err)
		}
		sw := subworkflow{Params: swj.Params}
		for _, rawStep := range swj.Steps {
			var stepMap map[string]json.RawMessage
			if err := json.Unmarshal(rawStep, &stepMap); err != nil {
				return nil, fmt.Errorf("subworkflow %q: step: %w", name, err)
			}
			if len(stepMap) != 1 {
				return nil, fmt.Errorf("subworkflow %q: each step must be a single-key object", name)
			}
			for stepName, bodyRaw := range stepMap {
				var body stepBodyJSON
				if err := json.Unmarshal(bodyRaw, &body); err != nil {
					return nil, fmt.Errorf("subworkflow %q: step %q: %w", name, stepName, err)
				}
				st := step{Name: stepName, Call: body.Call, Result: body.Result, Args: body.Args}
				for _, am := range body.Assign {
					if len(am) != 1 {
						return nil, fmt.Errorf("subworkflow %q: step %q: each assign entry must be a single-key object", name, stepName)
					}
					for k, v := range am {
						st.Assign = append(st.Assign, assignment{Name: k, Expr: v})
					}
				}
				for _, sc := range body.Switch {
					c := switchCase{ConditionExpr: stripExprMarkers(sc.Condition)}
					if sc.Next != nil {
						c.HasNext, c.Next = true, *sc.Next
					}
					if sc.Return != nil {
						c.HasReturn, c.Return = true, *sc.Return
					}
					st.Switch = append(st.Switch, c)
				}
				if body.Next != nil {
					st.HasNext, st.Next = true, *body.Next
				}
				if body.Return != nil {
					st.HasReturn, st.Return = true, *body.Return
				}
				sw.Steps = append(sw.Steps, st)
			}
		}
		def[name] = sw
	}
	return def, nil
}

func stripExprMarkers(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		return strings.TrimSpace(s[2 : len(s)-1])
	}
	return s
}

// evalRaw decodes a JSON value field (an assign expression, a return
// value, or a call argument): if it's a string of the form "${...}" it's
// evaluated as an expression against vars, otherwise the decoded JSON
// value itself is the literal result -- the same literal-vs-expression
// rule real Workflows source uses.
func evalRaw(raw json.RawMessage, vars map[string]any) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if s, ok := v.(string); ok {
		trimmed := strings.TrimSpace(s)
		if strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}") {
			return evalExpr(strings.TrimSpace(trimmed[2:len(trimmed)-1]), vars)
		}
	}
	return v, nil
}

// --- execution ----------------------------------------------------------

// interpreter holds the bits shared across an execution: the parsed
// document (so "call" can reach sibling subworkflows) and a step counter
// shared across subworkflow calls (so maxSteps bounds the whole
// execution, not just one subworkflow).
type interpreter struct {
	project string
	def     map[string]subworkflow
	steps   int
}

// runDefinition parses sourceContents as a JSON workflow definition and
// executes "main" with the given argument (the raw JSON text from
// executions.create's "argument" field). ok=false means sourceContents
// isn't a JSON definition this interpreter understands -- the caller
// should fall back to the previous echo behavior.
func runDefinition(project, sourceContents, argument string) (result string, errPayload string, ok bool) {
	def, err := parseDefinition(sourceContents)
	if err != nil {
		return "", "", false
	}
	main, found := def["main"]
	if !found {
		return "", "", false
	}
	in := &interpreter{project: project, def: def}
	val, rerr := in.runSubworkflow(main, decodeArgument(main, argument))
	if rerr != nil {
		return "", rerr.Error(), true
	}
	return encodeResult(val), "", true
}

// decodeArgument binds the execution's raw JSON argument string to the
// subworkflow's declared params, matching real Workflows binding rules:
// a single declared param receives the whole decoded argument; multiple
// declared params expect a JSON object and bind by key.
func decodeArgument(sw subworkflow, argument string) map[string]any {
	args := map[string]any{}
	if argument == "" {
		return args
	}
	var decoded any
	if err := json.Unmarshal([]byte(argument), &decoded); err != nil {
		if len(sw.Params) > 0 {
			args[sw.Params[0]] = argument
		}
		return args
	}
	if len(sw.Params) == 1 {
		args[sw.Params[0]] = decoded
		return args
	}
	if m, ok := decoded.(map[string]any); ok {
		for _, p := range sw.Params {
			args[p] = m[p]
		}
		return args
	}
	if len(sw.Params) > 0 {
		args[sw.Params[0]] = decoded
	}
	return args
}

func encodeResult(val any) string {
	if val == nil {
		return ""
	}
	b, err := json.Marshal(val)
	if err != nil {
		return ""
	}
	return string(b)
}

// switchOutcome is the result of evaluating a step's switch cases in
// order: the first case whose condition is true wins, and its own
// next/return (or lack of one, meaning "fall through") determines what
// happens next.
type switchOutcome struct {
	matched   bool
	terminal  bool
	hasReturn bool
	returnVal any
	hasJump   bool
	jumpPos   int
}

func evalSwitchCases(cases []switchCase, vars map[string]any, idx map[string]int) (switchOutcome, error) {
	for _, c := range cases {
		condVal, err := evalExpr(c.ConditionExpr, vars)
		if err != nil {
			return switchOutcome{}, err
		}
		if !toBool(condVal) {
			continue
		}
		if c.HasReturn {
			v, err := evalRaw(c.Return, vars)
			if err != nil {
				return switchOutcome{}, err
			}
			return switchOutcome{matched: true, hasReturn: true, returnVal: v}, nil
		}
		if c.HasNext {
			if c.Next == "end" {
				return switchOutcome{matched: true, terminal: true}, nil
			}
			p, ok := idx[c.Next]
			if !ok {
				return switchOutcome{}, fmt.Errorf("unknown step %q", c.Next)
			}
			return switchOutcome{matched: true, hasJump: true, jumpPos: p}, nil
		}
		return switchOutcome{matched: true}, nil
	}
	return switchOutcome{}, nil
}

// runSubworkflow executes one subworkflow (main, or a call target) to
// completion: sequential steps with assign/switch/next/return/call, until
// an explicit return, an explicit "next: end"/switch-into-"end", or the
// step list runs out (which ends the subworkflow with no value, matching
// the real API leaving result unset).
func (in *interpreter) runSubworkflow(sw subworkflow, args map[string]any) (any, error) {
	vars := map[string]any{}
	for _, p := range sw.Params {
		vars[p] = args[p]
	}
	idx := make(map[string]int, len(sw.Steps))
	for i, st := range sw.Steps {
		idx[st.Name] = i
	}

	pos := 0
	for pos < len(sw.Steps) {
		in.steps++
		if in.steps > maxSteps {
			return nil, fmt.Errorf("execution exceeded %d steps (possible infinite loop)", maxSteps)
		}
		st := sw.Steps[pos]

		for _, a := range st.Assign {
			v, err := evalRaw(a.Expr, vars)
			if err != nil {
				return nil, fmt.Errorf("step %q: assign %q: %w", st.Name, a.Name, err)
			}
			vars[a.Name] = v
		}

		if len(st.Switch) > 0 {
			outcome, err := evalSwitchCases(st.Switch, vars, idx)
			if err != nil {
				return nil, fmt.Errorf("step %q: switch: %w", st.Name, err)
			}
			if outcome.matched {
				switch {
				case outcome.hasReturn:
					return outcome.returnVal, nil
				case outcome.terminal:
					return nil, nil
				case outcome.hasJump:
					pos = outcome.jumpPos
					continue
				default:
					pos++
					continue
				}
			}
		}

		if st.HasReturn {
			v, err := evalRaw(st.Return, vars)
			if err != nil {
				return nil, fmt.Errorf("step %q: return: %w", st.Name, err)
			}
			return v, nil
		}

		if st.Call != "" {
			result, err := in.callStep(st, vars)
			if err != nil {
				return nil, fmt.Errorf("step %q: call %q: %w", st.Name, st.Call, err)
			}
			if st.Result != "" {
				vars[st.Result] = result
			}
		}

		if st.HasNext {
			if st.Next == "end" {
				return nil, nil
			}
			p, ok := idx[st.Next]
			if !ok {
				return nil, fmt.Errorf("step %q: next references unknown step %q", st.Name, st.Next)
			}
			pos = p
			continue
		}

		pos++
	}
	return nil, nil
}

// callStep evaluates a step's "call": either the sys.log builtin (the
// one real Workflows standard-library call simple enough to actually
// execute here -- it just needs activity.RecordLog, the same sink every
// other Phase 11 real-dispatch path already writes to) or another
// subworkflow defined in the same document. Real connectors/HTTP calls
// are out of scope and return a clear error instead of pretending to
// succeed.
func (in *interpreter) callStep(st step, vars map[string]any) (any, error) {
	args := map[string]any{}
	for k, raw := range st.Args {
		v, err := evalRaw(raw, vars)
		if err != nil {
			return nil, err
		}
		args[k] = v
	}

	if st.Call == "sys.log" {
		text := fmt.Sprint(args["text"])
		severity := "INFO"
		if sv, ok := args["severity"].(string); ok && sv != "" {
			severity = sv
		}
		if in.project != "" {
			activity.RecordLog(in.project, activity.LogEntry{
				LogName:     fmt.Sprintf("projects/%s/logs/workflows.googleapis.com%%2Fworkflow", in.project),
				Severity:    severity,
				TextPayload: text,
			})
		}
		return nil, nil
	}

	sub, ok := in.def[st.Call]
	if !ok {
		return nil, fmt.Errorf("unknown call target %q (only sys.log and same-document subworkflows are supported)", st.Call)
	}
	return in.runSubworkflow(sub, args)
}

// --- expression evaluator ------------------------------------------------
//
// Evaluates the "${...}" CEL-like subset Workflows conditions/values use:
// literals, variables (with dotted property access into decoded JSON
// objects), unary !/not/-, and the usual arithmetic/comparison/logical
// binary operators, with the conventional precedence (or < and < not <
// equality < comparison < additive < multiplicative < unary).

type token struct {
	kind string // "num" | "str" | "ident" | "op"
	text string
	num  float64
}

func tokenize(s string) ([]token, error) {
	var toks []token
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(' || c == ')' || c == ',' || c == '+' || c == '-' || c == '*' || c == '%' || c == '/':
			toks = append(toks, token{kind: "op", text: string(c)})
			i++
		case c == '!':
			if i+1 < n && s[i+1] == '=' {
				toks = append(toks, token{kind: "op", text: "!="})
				i += 2
			} else {
				toks = append(toks, token{kind: "op", text: "!"})
				i++
			}
		case c == '=':
			if i+1 < n && s[i+1] == '=' {
				toks = append(toks, token{kind: "op", text: "=="})
				i += 2
				break
			}
			return nil, fmt.Errorf("unexpected '=' at offset %d", i)
		case c == '<':
			if i+1 < n && s[i+1] == '=' {
				toks = append(toks, token{kind: "op", text: "<="})
				i += 2
			} else {
				toks = append(toks, token{kind: "op", text: "<"})
				i++
			}
		case c == '>':
			if i+1 < n && s[i+1] == '=' {
				toks = append(toks, token{kind: "op", text: ">="})
				i += 2
			} else {
				toks = append(toks, token{kind: "op", text: ">"})
				i++
			}
		case c == '&' && i+1 < n && s[i+1] == '&':
			toks = append(toks, token{kind: "op", text: "&&"})
			i += 2
		case c == '|' && i+1 < n && s[i+1] == '|':
			toks = append(toks, token{kind: "op", text: "||"})
			i += 2
		case c == '\'' || c == '"':
			quote := c
			j := i + 1
			var b strings.Builder
			for j < n && s[j] != quote {
				if s[j] == '\\' && j+1 < n {
					j++
				}
				b.WriteByte(s[j])
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("unterminated string literal in expression")
			}
			toks = append(toks, token{kind: "str", text: b.String()})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i
			for j < n && ((s[j] >= '0' && s[j] <= '9') || s[j] == '.') {
				j++
			}
			f, err := strconv.ParseFloat(s[i:j], 64)
			if err != nil {
				return nil, fmt.Errorf("invalid number %q in expression", s[i:j])
			}
			toks = append(toks, token{kind: "num", num: f, text: s[i:j]})
			i = j
		case isIdentStart(c):
			j := i
			for j < n && isIdentPart(s[j]) {
				j++
			}
			toks = append(toks, token{kind: "ident", text: s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q at offset %d", c, i)
		}
	}
	return toks, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '.'
}

type evaluator struct {
	toks []token
	pos  int
	vars map[string]any
}

func evalExpr(expr string, vars map[string]any) (any, error) {
	toks, err := tokenize(expr)
	if err != nil {
		return nil, fmt.Errorf("expression %q: %w", expr, err)
	}
	ev := &evaluator{toks: toks, vars: vars}
	v, err := ev.parseOr()
	if err != nil {
		return nil, fmt.Errorf("expression %q: %w", expr, err)
	}
	if ev.pos != len(ev.toks) {
		return nil, fmt.Errorf("expression %q: unexpected trailing token %q", expr, ev.peek().text)
	}
	return v, nil
}

func (ev *evaluator) peek() token {
	if ev.pos < len(ev.toks) {
		return ev.toks[ev.pos]
	}
	return token{kind: "eof"}
}

func (ev *evaluator) matchOp(texts ...string) bool {
	t := ev.peek()
	if t.kind != "op" && t.kind != "ident" {
		return false
	}
	for _, x := range texts {
		if t.text == x {
			ev.pos++
			return true
		}
	}
	return false
}

func (ev *evaluator) parseOr() (any, error) {
	left, err := ev.parseAnd()
	if err != nil {
		return nil, err
	}
	for ev.matchOp("||", "or") {
		right, err := ev.parseAnd()
		if err != nil {
			return nil, err
		}
		left = toBool(left) || toBool(right)
	}
	return left, nil
}

func (ev *evaluator) parseAnd() (any, error) {
	left, err := ev.parseNot()
	if err != nil {
		return nil, err
	}
	for ev.matchOp("&&", "and") {
		right, err := ev.parseNot()
		if err != nil {
			return nil, err
		}
		left = toBool(left) && toBool(right)
	}
	return left, nil
}

func (ev *evaluator) parseNot() (any, error) {
	if ev.matchOp("!", "not") {
		v, err := ev.parseNot()
		if err != nil {
			return nil, err
		}
		return !toBool(v), nil
	}
	return ev.parseEquality()
}

func (ev *evaluator) parseEquality() (any, error) {
	left, err := ev.parseComparison()
	if err != nil {
		return nil, err
	}
	for {
		if ev.matchOp("==") {
			right, err := ev.parseComparison()
			if err != nil {
				return nil, err
			}
			left = reflect.DeepEqual(left, right)
		} else if ev.matchOp("!=") {
			right, err := ev.parseComparison()
			if err != nil {
				return nil, err
			}
			left = !reflect.DeepEqual(left, right)
		} else {
			return left, nil
		}
	}
}

func (ev *evaluator) parseComparison() (any, error) {
	left, err := ev.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		var op string
		switch {
		case ev.matchOp("<="):
			op = "<="
		case ev.matchOp(">="):
			op = ">="
		case ev.matchOp("<"):
			op = "<"
		case ev.matchOp(">"):
			op = ">"
		default:
			return left, nil
		}
		right, err := ev.parseAdditive()
		if err != nil {
			return nil, err
		}
		l, r, err := numPair(left, right)
		if err != nil {
			return nil, err
		}
		switch op {
		case "<=":
			left = l <= r
		case ">=":
			left = l >= r
		case "<":
			left = l < r
		case ">":
			left = l > r
		}
	}
}

func (ev *evaluator) parseAdditive() (any, error) {
	left, err := ev.parseTerm()
	if err != nil {
		return nil, err
	}
	for {
		if ev.matchOp("+") {
			right, err := ev.parseTerm()
			if err != nil {
				return nil, err
			}
			left, err = addValues(left, right)
			if err != nil {
				return nil, err
			}
		} else if ev.matchOp("-") {
			right, err := ev.parseTerm()
			if err != nil {
				return nil, err
			}
			l, r, err := numPair(left, right)
			if err != nil {
				return nil, err
			}
			left = l - r
		} else {
			return left, nil
		}
	}
}

func (ev *evaluator) parseTerm() (any, error) {
	left, err := ev.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		if ev.matchOp("*") {
			right, err := ev.parseUnary()
			if err != nil {
				return nil, err
			}
			l, r, err := numPair(left, right)
			if err != nil {
				return nil, err
			}
			left = l * r
		} else if ev.matchOp("/") {
			right, err := ev.parseUnary()
			if err != nil {
				return nil, err
			}
			l, r, err := numPair(left, right)
			if err != nil {
				return nil, err
			}
			if r == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			left = l / r
		} else if ev.matchOp("%") {
			right, err := ev.parseUnary()
			if err != nil {
				return nil, err
			}
			l, r, err := numPair(left, right)
			if err != nil {
				return nil, err
			}
			if r == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			left = math.Mod(l, r)
		} else {
			return left, nil
		}
	}
}

func (ev *evaluator) parseUnary() (any, error) {
	if ev.matchOp("-") {
		v, err := ev.parsePrimary()
		if err != nil {
			return nil, err
		}
		f, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("unary '-' requires a numeric operand")
		}
		return -f, nil
	}
	return ev.parsePrimary()
}

func (ev *evaluator) parsePrimary() (any, error) {
	t := ev.peek()
	ev.pos++
	switch t.kind {
	case "num":
		return t.num, nil
	case "str":
		return t.text, nil
	case "ident":
		switch t.text {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		}
		return ev.lookupVar(t.text)
	case "op":
		if t.text == "(" {
			v, err := ev.parseOr()
			if err != nil {
				return nil, err
			}
			if !ev.matchOp(")") {
				return nil, fmt.Errorf("expected ')'")
			}
			return v, nil
		}
	}
	return nil, fmt.Errorf("unexpected token %q", t.text)
}

func (ev *evaluator) lookupVar(path string) (any, error) {
	parts := strings.Split(path, ".")
	v, ok := ev.vars[parts[0]]
	if !ok {
		return nil, fmt.Errorf("undefined variable %q", parts[0])
	}
	for _, p := range parts[1:] {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot access property %q of a non-object value", p)
		}
		v = m[p]
	}
	return v, nil
}

func toBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case nil:
		return false
	case float64:
		return x != 0
	case string:
		return x != ""
	default:
		return true
	}
}

func numPair(a, b any) (float64, float64, error) {
	af, aok := a.(float64)
	bf, bok := b.(float64)
	if !aok || !bok {
		return 0, 0, fmt.Errorf("operator requires numeric operands")
	}
	return af, bf, nil
}

// addValues implements "+": numeric addition for two numbers, or
// concatenation for two strings -- real Workflows expressions (CEL) are
// strict about not silently coercing between the two, so a string+number
// mismatch is a clear error rather than an implicit stringification.
func addValues(a, b any) (any, error) {
	if as, ok := a.(string); ok {
		if bs, ok := b.(string); ok {
			return as + bs, nil
		}
		return nil, fmt.Errorf("'+' requires both operands to be strings or both numbers")
	}
	af, aok := a.(float64)
	bf, bok := b.(float64)
	if aok && bok {
		return af + bf, nil
	}
	return nil, fmt.Errorf("'+' requires both operands to be strings or both numbers")
}
