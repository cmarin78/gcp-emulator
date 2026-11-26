package sqlengine

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Query executes a SQL query statement against the engine.
func (e *Engine) Query(sqlStr string) (*QueryResult, error) {
	stmtAny, err := Parse(sqlStr)
	if err != nil {
		return nil, fmt.Errorf("SQL parse error: %w", err)
	}

	stmt, ok := stmtAny.(*SelectStmt)
	if !ok {
		return nil, fmt.Errorf("statement is not a SELECT query")
	}

	tbl, ok := e.GetTable(stmt.Table)
	if !ok {
		return nil, fmt.Errorf("table not found: %s", stmt.Table)
	}

	tbl.mu.RLock()
	defer tbl.mu.RUnlock()

	// 1. Filter rows using WHERE
	var filtered []map[string]any
	for _, row := range tbl.Rows {
		if stmt.Where != nil {
			match, err := evalCondition(stmt.Where, row)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
		}
		filtered = append(filtered, row)
	}

	// 2. Check for Aggregate Functions
	hasAggregates := false
	for _, it := range stmt.Items {
		if _, isFunc := it.Expr.(FuncCallExpr); isFunc {
			hasAggregates = true
			break
		}
	}

	var resCols []Column
	var resRows [][]any

	if hasAggregates || len(stmt.GroupBy) > 0 {
		resCols, resRows, err = e.evalAggregates(stmt, filtered, tbl)
		if err != nil {
			return nil, err
		}
		// 4. ORDER BY (for aggregates)
		if len(stmt.OrderBy) > 0 {
			sortRows(resCols, resRows, stmt.OrderBy)
		}
	} else {
		// 4. ORDER BY (for non-aggregates, can sort by any table column)
		if len(stmt.OrderBy) > 0 {
			sortFilteredRows(filtered, stmt.OrderBy)
		}
		resCols, resRows, err = e.evalProjections(stmt, filtered, tbl)
		if err != nil {
			return nil, err
		}
	}

	// 3. DISTINCT
	if stmt.Distinct {
		resRows = deduplicateRows(resRows)
	}

	// 5. LIMIT / OFFSET
	total := len(resRows)
	if stmt.Offset > 0 {
		if stmt.Offset >= total {
			resRows = [][]any{}
		} else {
			resRows = resRows[stmt.Offset:]
		}
	}
	if stmt.Limit >= 0 && len(resRows) > stmt.Limit {
		resRows = resRows[:stmt.Limit]
	}

	return &QueryResult{
		Columns:   resCols,
		Rows:      resRows,
		TotalRows: len(resRows),
	}, nil
}

// Execute runs a DDL or DML statement against the engine.
func (e *Engine) Execute(sqlStr string) (*ExecResult, error) {
	stmtAny, err := Parse(sqlStr)
	if err != nil {
		return nil, fmt.Errorf("SQL parse error: %w", err)
	}

	switch stmt := stmtAny.(type) {
	case *CreateTableStmt:
		if err := e.CreateTable(stmt.Table, stmt.Columns); err != nil {
			return nil, err
		}
		return &ExecResult{}, nil

	case *DropTableStmt:
		if err := e.DropTable(stmt.Table); err != nil {
			return nil, err
		}
		return &ExecResult{}, nil

	case *InsertStmt:
		tbl, ok := e.GetTable(stmt.Table)
		if !ok {
			return nil, fmt.Errorf("table not found: %s", stmt.Table)
		}
		var newRows []map[string]any
		for _, valRow := range stmt.Values {
			rowMap := make(map[string]any)
			for i, valExpr := range valRow {
				val, err := evalExpr(valExpr, nil)
				if err != nil {
					return nil, err
				}
				colName := fmt.Sprintf("col_%d", i)
				if i < len(stmt.Columns) {
					colName = stmt.Columns[i]
				} else if i < len(tbl.Columns) {
					colName = tbl.Columns[i].Name
				}
				rowMap[colName] = val
			}
			newRows = append(newRows, rowMap)
		}
		if err := e.InsertRows(stmt.Table, newRows); err != nil {
			return nil, err
		}
		return &ExecResult{RowsAffected: int64(len(newRows))}, nil

	case *UpdateStmt:
		tbl, ok := e.GetTable(stmt.Table)
		if !ok {
			return nil, fmt.Errorf("table not found: %s", stmt.Table)
		}
		tbl.mu.Lock()
		defer tbl.mu.Unlock()

		var updatedCount int64
		for _, row := range tbl.Rows {
			if stmt.Where != nil {
				match, err := evalCondition(stmt.Where, row)
				if err != nil {
					return nil, err
				}
				if !match {
					continue
				}
			}
			for col, expr := range stmt.Assignments {
				newVal, err := evalExpr(expr, row)
				if err != nil {
					return nil, err
				}
				row[col] = newVal
			}
			updatedCount++
		}
		return &ExecResult{RowsAffected: updatedCount}, nil

	case *DeleteStmt:
		tbl, ok := e.GetTable(stmt.Table)
		if !ok {
			return nil, fmt.Errorf("table not found: %s", stmt.Table)
		}
		tbl.mu.Lock()
		defer tbl.mu.Unlock()

		var remaining []map[string]any
		var deletedCount int64
		for _, row := range tbl.Rows {
			if stmt.Where != nil {
				match, err := evalCondition(stmt.Where, row)
				if err != nil {
					return nil, err
				}
				if match {
					deletedCount++
					continue
				}
			} else {
				deletedCount++
				continue
			}
			remaining = append(remaining, row)
		}
		tbl.Rows = remaining
		return &ExecResult{RowsAffected: deletedCount}, nil

	default:
		return nil, fmt.Errorf("unsupported statement type for Execute: %T", stmtAny)
	}
}

// ── Projections (non-aggregate) ───────────────────────────────────────────────

func (e *Engine) evalProjections(stmt *SelectStmt, rows []map[string]any, tbl *Table) ([]Column, [][]any, error) {
	// 1. Resolve columns
	var cols []Column
	isStar := len(stmt.Items) == 1 && isStarExpr(stmt.Items[0].Expr)

	if isStar {
		for _, c := range tbl.Columns {
			cols = append(cols, c)
		}
	} else {
		for i, it := range stmt.Items {
			name := it.Alias
			if name == "" {
				if colRef, ok := it.Expr.(ColRefExpr); ok {
					name = colRef.Name
				} else {
					name = fmt.Sprintf("col_%d", i+1)
				}
			}
			cols = append(cols, Column{Name: name, Type: "STRING"})
		}
	}

	// 2. Project rows
	var resultRows [][]any
	for _, r := range rows {
		rowCells := make([]any, len(cols))
		if isStar {
			for j, c := range cols {
				rowCells[j] = getCaseInsensitive(r, c.Name)
			}
		} else {
			for j, it := range stmt.Items {
				val, err := evalExpr(it.Expr, r)
				if err != nil {
					return nil, nil, err
				}
				rowCells[j] = val
			}
		}
		resultRows = append(resultRows, rowCells)
	}

	return cols, resultRows, nil
}

// ── Aggregates (COUNT, SUM, AVG, MIN, MAX) ───────────────────────────────────

func (e *Engine) evalAggregates(stmt *SelectStmt, rows []map[string]any, tbl *Table) ([]Column, [][]any, error) {
	var cols []Column
	for i, it := range stmt.Items {
		name := it.Alias
		if name == "" {
			if fn, ok := it.Expr.(FuncCallExpr); ok {
				argName := "*"
				if colRef, ok := fn.Arg.(ColRefExpr); ok {
					argName = colRef.Name
				}
				name = fmt.Sprintf("%s(%s)", strings.ToUpper(fn.Name), argName)
			} else if colRef, ok := it.Expr.(ColRefExpr); ok {
				name = colRef.Name
			} else {
				name = fmt.Sprintf("col_%d", i+1)
			}
		}
		cols = append(cols, Column{Name: name, Type: "STRING"})
	}

	// Group rows
	type groupKey string
	groups := make(map[groupKey][]map[string]any)
	var groupOrder []groupKey

	if len(stmt.GroupBy) > 0 {
		for _, r := range rows {
			var keyParts []string
			for _, gCol := range stmt.GroupBy {
				v := getCaseInsensitive(r, gCol)
				keyParts = append(keyParts, fmt.Sprintf("%v", v))
			}
			k := groupKey(strings.Join(keyParts, "::"))
			if _, exists := groups[k]; !exists {
				groupOrder = append(groupOrder, k)
			}
			groups[k] = append(groups[k], r)
		}
	} else {
		// Single group for entire table
		groups["all"] = rows
		groupOrder = append(groupOrder, "all")
	}

	var resRows [][]any
	for _, gk := range groupOrder {
		groupRows := groups[gk]
		rowCells := make([]any, len(stmt.Items))
		for j, it := range stmt.Items {
			if fn, ok := it.Expr.(FuncCallExpr); ok {
				val, err := computeAggregate(fn, groupRows)
				if err != nil {
					return nil, nil, err
				}
				rowCells[j] = val
			} else {
				// Regular column: take value from first row of group
				if len(groupRows) > 0 {
					val, _ := evalExpr(it.Expr, groupRows[0])
					rowCells[j] = val
				} else {
					rowCells[j] = nil
				}
			}
		}
		resRows = append(resRows, rowCells)
	}

	return cols, resRows, nil
}

func computeAggregate(fn FuncCallExpr, rows []map[string]any) (any, error) {
	fnName := strings.ToUpper(fn.Name)
	argCol := ""
	if colRef, ok := fn.Arg.(ColRefExpr); ok {
		argCol = colRef.Name
	}

	switch fnName {
	case "COUNT":
		if argCol == "*" || argCol == "" {
			return int64(len(rows)), nil
		}
		var count int64
		for _, r := range rows {
			if v := getCaseInsensitive(r, argCol); v != nil {
				count++
			}
		}
		return count, nil

	case "SUM":
		var sum float64
		for _, r := range rows {
			v := getCaseInsensitive(r, argCol)
			num, ok := toFloat(v)
			if ok {
				sum += num
			}
		}
		if math.Floor(sum) == sum {
			return int64(sum), nil
		}
		return sum, nil

	case "AVG":
		if len(rows) == 0 {
			return nil, nil
		}
		var sum float64
		var count int64
		for _, r := range rows {
			v := getCaseInsensitive(r, argCol)
			num, ok := toFloat(v)
			if ok {
				sum += num
				count++
			}
		}
		if count == 0 {
			return nil, nil
		}
		return sum / float64(count), nil

	case "MIN":
		var minVal any
		for _, r := range rows {
			v := getCaseInsensitive(r, argCol)
			if v == nil {
				continue
			}
			if minVal == nil || compareAny(v, minVal) < 0 {
				minVal = v
			}
		}
		return minVal, nil

	case "MAX":
		var maxVal any
		for _, r := range rows {
			v := getCaseInsensitive(r, argCol)
			if v == nil {
				continue
			}
			if maxVal == nil || compareAny(v, maxVal) > 0 {
				maxVal = v
			}
		}
		return maxVal, nil

	default:
		return nil, fmt.Errorf("unsupported aggregate function: %s", fnName)
	}
}

// ── Expression Evaluation ─────────────────────────────────────────────────────

func evalCondition(expr Expr, row map[string]any) (bool, error) {
	val, err := evalExpr(expr, row)
	if err != nil {
		return false, err
	}
	return toBool(val), nil
}

func evalExpr(expr Expr, row map[string]any) (any, error) {
	if expr == nil {
		return nil, nil
	}

	switch e := expr.(type) {
	case LiteralExpr:
		return e.Value, nil

	case ColRefExpr:
		if e.Name == "*" {
			return "*", nil
		}
		return getCaseInsensitive(row, e.Name), nil

	case UnaryExpr:
		if strings.EqualFold(e.Op, "NOT") {
			sub, err := evalExpr(e.Expr, row)
			if err != nil {
				return nil, err
			}
			return !toBool(sub), nil
		}
		return nil, fmt.Errorf("unknown unary operator: %s", e.Op)

	case BinaryExpr:
		op := strings.ToUpper(e.Op)
		if op == "AND" {
			leftVal, err := evalExpr(e.Left, row)
			if err != nil || !toBool(leftVal) {
				return false, err
			}
			rightVal, err := evalExpr(e.Right, row)
			if err != nil {
				return false, err
			}
			return toBool(rightVal), nil
		}
		if op == "OR" {
			leftVal, err := evalExpr(e.Left, row)
			if err == nil && toBool(leftVal) {
				return true, nil
			}
			rightVal, err := evalExpr(e.Right, row)
			if err != nil {
				return false, err
			}
			return toBool(rightVal), nil
		}

		leftVal, err := evalExpr(e.Left, row)
		if err != nil {
			return nil, err
		}
		rightVal, err := evalExpr(e.Right, row)
		if err != nil {
			return nil, err
		}

		switch op {
		case "=":
			return compareAny(leftVal, rightVal) == 0, nil
		case "!=", "<>":
			return compareAny(leftVal, rightVal) != 0, nil
		case "<":
			return compareAny(leftVal, rightVal) < 0, nil
		case "<=":
			return compareAny(leftVal, rightVal) <= 0, nil
		case ">":
			return compareAny(leftVal, rightVal) > 0, nil
		case ">=":
			return compareAny(leftVal, rightVal) >= 0, nil
		case "+":
			lf, lok := toFloat(leftVal)
			rf, rok := toFloat(rightVal)
			if lok && rok {
				sum := lf + rf
				if math.Floor(sum) == sum {
					return int64(sum), nil
				}
				return sum, nil
			}
			return fmt.Sprintf("%v%v", leftVal, rightVal), nil
		case "-":
			lf, _ := toFloat(leftVal)
			rf, _ := toFloat(rightVal)
			return lf - rf, nil
		default:
			return nil, fmt.Errorf("unsupported binary operator: %s", op)
		}

	case IsNullExpr:
		val, err := evalExpr(e.Expr, row)
		if err != nil {
			return nil, err
		}
		isNull := val == nil
		if e.Not {
			return !isNull, nil
		}
		return isNull, nil

	case LikeExpr:
		val, err := evalExpr(e.Expr, row)
		if err != nil {
			return nil, err
		}
		patternVal, err := evalExpr(e.Pattern, row)
		if err != nil {
			return nil, err
		}
		match := matchLike(fmt.Sprintf("%v", val), fmt.Sprintf("%v", patternVal))
		if e.Not {
			return !match, nil
		}
		return match, nil

	case InExpr:
		val, err := evalExpr(e.Expr, row)
		if err != nil {
			return nil, err
		}
		matched := false
		for _, itemExpr := range e.Values {
			itemVal, err := evalExpr(itemExpr, row)
			if err != nil {
				return nil, err
			}
			if compareAny(val, itemVal) == 0 {
				matched = true
				break
			}
		}
		if e.Not {
			return !matched, nil
		}
		return matched, nil

	default:
		return nil, fmt.Errorf("unsupported expression: %T", expr)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func getCaseInsensitive(row map[string]any, col string) any {
	if row == nil {
		return nil
	}
	colLower := strings.ToLower(col)
	for k, v := range row {
		if strings.ToLower(k) == colLower {
			return v
		}
	}
	return nil
}

func isStarExpr(expr Expr) bool {
	if c, ok := expr.(ColRefExpr); ok && c.Name == "*" {
		return true
	}
	return false
}

func toBool(v any) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case int, int64, int32:
		return val != 0
	case float64, float32:
		return val != 0
	case string:
		upper := strings.ToUpper(val)
		return upper == "TRUE" || upper == "1" || upper == "YES"
	default:
		return false
	}
}

func toFloat(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case int32:
		return float64(val), true
	case string:
		f, err := strconv.ParseFloat(val, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func compareAny(a, b any) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}

	af, aOk := toFloat(a)
	bf, bOk := toFloat(b)
	if aOk && bOk {
		if af < bf {
			return -1
		}
		if af > bf {
			return 1
		}
		return 0
	}

	as := fmt.Sprintf("%v", a)
	bs := fmt.Sprintf("%v", b)
	return strings.Compare(as, bs)
}

func matchLike(str, pattern string) bool {
	var sb strings.Builder
	sb.WriteString("(?i)^")
	for _, ch := range pattern {
		switch ch {
		case '%':
			sb.WriteString(".*")
		case '_':
			sb.WriteString(".")
		default:
			sb.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return false
	}
	return re.MatchString(str)
}

func deduplicateRows(rows [][]any) [][]any {
	seen := make(map[string]bool)
	var deduped [][]any
	for _, r := range rows {
		key := fmt.Sprintf("%v", r)
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, r)
		}
	}
	return deduped
}

func sortRows(cols []Column, rows [][]any, orderBys []OrderItem) {
	colIndex := make(map[string]int)
	for i, c := range cols {
		colIndex[strings.ToLower(c.Name)] = i
	}

	sort.SliceStable(rows, func(i, j int) bool {
		for _, ob := range orderBys {
			idx, ok := colIndex[strings.ToLower(ob.Column)]
			if !ok {
				continue
			}
			cmp := compareAny(rows[i][idx], rows[j][idx])
			if cmp == 0 {
				continue
			}
			if ob.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
}

func sortFilteredRows(rows []map[string]any, orderBys []OrderItem) {
	sort.SliceStable(rows, func(i, j int) bool {
		for _, ob := range orderBys {
			vi := getCaseInsensitive(rows[i], ob.Column)
			vj := getCaseInsensitive(rows[j], ob.Column)
			cmp := compareAny(vi, vj)
			if cmp == 0 {
				continue
			}
			if ob.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
}
