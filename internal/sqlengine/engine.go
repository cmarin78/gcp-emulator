package sqlengine

import (
	"fmt"
	"strings"
	"sync"
)

// Column defines a table column name and logical type.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"` // STRING, INT64, FLOAT64, BOOL, BYTES, etc.
}

// Table represents an in-memory SQL table.
type Table struct {
	Name    string
	Columns []Column
	Rows    []map[string]any
	mu      sync.RWMutex
}

// QueryResult represents the outcome of a SELECT query.
type QueryResult struct {
	Columns   []Column `json:"columns"`
	Rows      [][]any  `json:"rows"`
	TotalRows int      `json:"totalRows"`
}

// ToMaps converts the row cells into an array of column-keyed maps.
func (qr *QueryResult) ToMaps() []map[string]any {
	result := make([]map[string]any, len(qr.Rows))
	for i, r := range qr.Rows {
		m := make(map[string]any, len(qr.Columns))
		for j, col := range qr.Columns {
			if j < len(r) {
				m[col.Name] = r[j]
			}
		}
		result[i] = m
	}
	return result
}

// ExecResult represents the outcome of a DDL/DML statement.
type ExecResult struct {
	RowsAffected int64 `json:"rowsAffected"`
	LastInsertID int64 `json:"lastInsertId"`
}

// Engine is the thread-safe in-memory SQL execution engine.
type Engine struct {
	tables map[string]*Table
	mu     sync.RWMutex
}

// New creates a new in-memory SQL engine.
func New() *Engine {
	return &Engine{
		tables: make(map[string]*Table),
	}
}

// normalizeName converts table names to a canonical lowercase representation.
func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "`\"")
	return strings.ToLower(name)
}

// CreateTable registers a new table schema with optional initial columns.
func (e *Engine) CreateTable(name string, cols []Column) error {
	norm := normalizeName(name)
	if norm == "" {
		return fmt.Errorf("table name cannot be empty")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.tables[norm] = &Table{
		Name:    name,
		Columns: cols,
		Rows:    make([]map[string]any, 0),
	}
	return nil
}

// DropTable removes a table from the engine.
func (e *Engine) DropTable(name string) error {
	norm := normalizeName(name)
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.tables, norm)
	return nil
}

// GetTable retrieves a table by name.
func (e *Engine) GetTable(name string) (*Table, bool) {
	norm := normalizeName(name)
	e.mu.RLock()
	defer e.mu.RUnlock()

	tbl, ok := e.tables[norm]
	return tbl, ok
}

// InsertRow appends a row into the specified table.
func (e *Engine) InsertRow(tableName string, row map[string]any) error {
	return e.InsertRows(tableName, []map[string]any{row})
}

// InsertRows appends multiple rows into the specified table.
func (e *Engine) InsertRows(tableName string, rows []map[string]any) error {
	norm := normalizeName(tableName)
	e.mu.Lock()
	tbl, ok := e.tables[norm]
	if !ok {
		// Auto-create table if columns can be inferred
		var cols []Column
		if len(rows) > 0 {
			for k := range rows[0] {
				cols = append(cols, Column{Name: k, Type: "STRING"})
			}
		}
		tbl = &Table{
			Name:    tableName,
			Columns: cols,
			Rows:    make([]map[string]any, 0),
		}
		e.tables[norm] = tbl
	}
	e.mu.Unlock()

	tbl.mu.Lock()
	defer tbl.mu.Unlock()

	// Update columns if new keys appear
	existingCols := make(map[string]bool)
	for _, c := range tbl.Columns {
		existingCols[strings.ToLower(c.Name)] = true
	}
	for _, r := range rows {
		for k := range r {
			if !existingCols[strings.ToLower(k)] {
				tbl.Columns = append(tbl.Columns, Column{Name: k, Type: "STRING"})
				existingCols[strings.ToLower(k)] = true
			}
		}
		// Clone row map
		rowCopy := make(map[string]any, len(r))
		for k, v := range r {
			rowCopy[k] = v
		}
		tbl.Rows = append(tbl.Rows, rowCopy)
	}
	return nil
}

// TruncateTable removes all rows from a table while preserving its schema.
func (e *Engine) TruncateTable(tableName string) error {
	norm := normalizeName(tableName)
	e.mu.RLock()
	tbl, ok := e.tables[norm]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("table not found: %s", tableName)
	}

	tbl.mu.Lock()
	defer tbl.mu.Unlock()
	tbl.Rows = make([]map[string]any, 0)
	return nil
}
