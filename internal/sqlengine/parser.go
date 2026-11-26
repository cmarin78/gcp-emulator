package sqlengine

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// TokenType represents lexical token types.
type TokenType int

const (
	tokEOF TokenType = iota
	tokIdent
	tokString
	tokNumber
	tokBool
	tokNull
	tokComma
	tokLParen
	tokRParen
	tokSemicolon
	tokStar
	tokOp // =, !=, <>, <, <=, >, >=, +, -, /
	tokKeyword
)

// Token represents a single lexical token.
type Token struct {
	Type  TokenType
	Value string
}

// Tokenizer breaks a SQL query into tokens.
type Tokenizer struct {
	input []rune
	pos   int
}

func newTokenizer(sql string) *Tokenizer {
	return &Tokenizer{input: []rune(sql), pos: 0}
}

func (t *Tokenizer) peek() rune {
	if t.pos >= len(t.input) {
		return 0
	}
	return t.input[t.pos]
}

func (t *Tokenizer) next() rune {
	if t.pos >= len(t.input) {
		return 0
	}
	ch := t.input[t.pos]
	t.pos++
	return ch
}

func (t *Tokenizer) skipWhitespace() {
	for t.pos < len(t.input) {
		ch := t.input[t.pos]
		if unicode.IsSpace(ch) {
			t.pos++
			continue
		}
		// Skip line comments -- ...
		if ch == '-' && t.pos+1 < len(t.input) && t.input[t.pos+1] == '-' {
			t.pos += 2
			for t.pos < len(t.input) && t.input[t.pos] != '\n' {
				t.pos++
			}
			continue
		}
		break
	}
}

func isIdentStart(ch rune) bool {
	return unicode.IsLetter(ch) || ch == '_' || ch == '@'
}

func isIdentPart(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '.' || ch == '-' || ch == ':'
}

var keywords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "AND": true, "OR": true,
	"NOT": true, "ORDER": true, "BY": true, "ASC": true, "DESC": true,
	"GROUP": true, "LIMIT": true, "OFFSET": true, "INSERT": true, "INTO": true,
	"VALUES": true, "UPDATE": true, "SET": true, "DELETE": true, "CREATE": true,
	"TABLE": true, "DROP": true, "AS": true, "LIKE": true, "IN": true,
	"IS": true, "NULL": true, "TRUE": true, "FALSE": true, "COUNT": true,
	"SUM": true, "AVG": true, "MIN": true, "MAX": true, "HAVING": true,
	"DISTINCT": true,
}

func (t *Tokenizer) NextToken() (Token, error) {
	t.skipWhitespace()
	if t.pos >= len(t.input) {
		return Token{Type: tokEOF}, nil
	}

	ch := t.peek()

	// Strings: '...' or "..."
	if ch == '\'' || ch == '"' {
		quote := t.next()
		var sb strings.Builder
		for {
			if t.pos >= len(t.input) {
				return Token{}, fmt.Errorf("unterminated string literal")
			}
			c := t.next()
			if c == quote {
				if t.peek() == quote { // escaped ''
					t.next()
					sb.WriteRune(quote)
					continue
				}
				break
			}
			if c == '\\' && t.pos < len(t.input) {
				c = t.next()
			}
			sb.WriteRune(c)
		}
		return Token{Type: tokString, Value: sb.String()}, nil
	}

	// Backtick identifiers: `...`
	if ch == '`' {
		t.next()
		var sb strings.Builder
		for {
			if t.pos >= len(t.input) {
				return Token{}, fmt.Errorf("unterminated backtick identifier")
			}
			c := t.next()
			if c == '`' {
				break
			}
			sb.WriteRune(c)
		}
		return Token{Type: tokIdent, Value: sb.String()}, nil
	}

	// Delimiters
	switch ch {
	case ',':
		t.next()
		return Token{Type: tokComma, Value: ","}, nil
	case '(':
		t.next()
		return Token{Type: tokLParen, Value: "("}, nil
	case ')':
		t.next()
		return Token{Type: tokRParen, Value: ")"}, nil
	case ';':
		t.next()
		return Token{Type: tokSemicolon, Value: ";"}, nil
	case '*':
		t.next()
		return Token{Type: tokStar, Value: "*"}, nil
	}

	// Comparison and Math Operators
	if ch == '=' || ch == '+' || ch == '/' || ch == '-' {
		t.next()
		return Token{Type: tokOp, Value: string(ch)}, nil
	}
	if ch == '!' {
		t.next()
		if t.peek() == '=' {
			t.next()
			return Token{Type: tokOp, Value: "!="}, nil
		}
		return Token{Type: tokOp, Value: "!"}, nil
	}
	if ch == '<' {
		t.next()
		if t.peek() == '=' {
			t.next()
			return Token{Type: tokOp, Value: "<="}, nil
		}
		if t.peek() == '>' {
			t.next()
			return Token{Type: tokOp, Value: "<>"}, nil
		}
		return Token{Type: tokOp, Value: "<"}, nil
	}
	if ch == '>' {
		t.next()
		if t.peek() == '=' {
			t.next()
			return Token{Type: tokOp, Value: ">="}, nil
		}
		return Token{Type: tokOp, Value: ">"}, nil
	}

	// Numbers
	if unicode.IsDigit(ch) {
		var sb strings.Builder
		hasDot := false
		for t.pos < len(t.input) {
			c := t.peek()
			if unicode.IsDigit(c) {
				sb.WriteRune(t.next())
			} else if c == '.' && !hasDot {
				hasDot = true
				sb.WriteRune(t.next())
			} else {
				break
			}
		}
		return Token{Type: tokNumber, Value: sb.String()}, nil
	}

	// Identifiers or Keywords
	if isIdentStart(ch) {
		var sb strings.Builder
		for t.pos < len(t.input) && isIdentPart(t.peek()) {
			sb.WriteRune(t.next())
		}
		str := sb.String()
		upper := strings.ToUpper(str)
		if upper == "TRUE" || upper == "FALSE" {
			return Token{Type: tokBool, Value: upper}, nil
		}
		if upper == "NULL" {
			return Token{Type: tokNull, Value: "NULL"}, nil
		}
		if keywords[upper] {
			return Token{Type: tokKeyword, Value: upper}, nil
		}
		return Token{Type: tokIdent, Value: str}, nil
	}

	return Token{}, fmt.Errorf("unexpected character: %c", t.next())
}

// ── AST Definition ────────────────────────────────────────────────────────────

type Expr interface {
	exprNode()
}

type (
	BinaryExpr struct {
		Op    string
		Left  Expr
		Right Expr
	}
	UnaryExpr struct {
		Op   string
		Expr Expr
	}
	ColRefExpr struct {
		Name string
	}
	LiteralExpr struct {
		Value any
	}
	FuncCallExpr struct {
		Name string
		Arg  Expr // could be ColRefExpr or "*" for COUNT(*)
	}
	InExpr struct {
		Expr   Expr
		Values []Expr
		Not    bool
	}
	IsNullExpr struct {
		Expr Expr
		Not  bool
	}
	LikeExpr struct {
		Expr    Expr
		Pattern Expr
		Not     bool
	}
)

func (BinaryExpr) exprNode()   {}
func (UnaryExpr) exprNode()    {}
func (ColRefExpr) exprNode()   {}
func (LiteralExpr) exprNode()  {}
func (FuncCallExpr) exprNode() {}
func (InExpr) exprNode()       {}
func (IsNullExpr) exprNode()   {}
func (LikeExpr) exprNode()     {}

type SelectItem struct {
	Expr  Expr
	Alias string
}

type OrderItem struct {
	Column string
	Desc   bool
}

type SelectStmt struct {
	Distinct bool
	Items    []SelectItem
	Table    string
	Where    Expr
	GroupBy  []string
	OrderBy  []OrderItem
	Limit    int
	Offset   int
}

type InsertStmt struct {
	Table   string
	Columns []string
	Values  [][]Expr
}

type UpdateStmt struct {
	Table       string
	Assignments map[string]Expr
	Where       Expr
}

type DeleteStmt struct {
	Table string
	Where Expr
}

type CreateTableStmt struct {
	Table   string
	Columns []Column
}

type DropTableStmt struct {
	Table string
}

// ── Parser ────────────────────────────────────────────────────────────────────

type Parser struct {
	tokens []Token
	pos    int
}

func Parse(sql string) (any, error) {
	tz := newTokenizer(sql)
	var tokens []Token
	for {
		tok, err := tz.NextToken()
		if err != nil {
			return nil, err
		}
		if tok.Type == tokEOF {
			break
		}
		tokens = append(tokens, tok)
	}

	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty SQL statement")
	}

	p := &Parser{tokens: tokens, pos: 0}
	first := p.peek()
	if first.Type != tokKeyword {
		return nil, fmt.Errorf("statement must begin with a SQL keyword: %s", first.Value)
	}

	switch first.Value {
	case "SELECT":
		return p.parseSelect()
	case "INSERT":
		return p.parseInsert()
	case "UPDATE":
		return p.parseUpdate()
	case "DELETE":
		return p.parseDelete()
	case "CREATE":
		return p.parseCreateTable()
	case "DROP":
		return p.parseDropTable()
	default:
		return nil, fmt.Errorf("unsupported SQL command: %s", first.Value)
	}
}

func (p *Parser) peek() Token {
	if p.pos >= len(p.tokens) {
		return Token{Type: tokEOF}
	}
	return p.tokens[p.pos]
}

func (p *Parser) next() Token {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

func (p *Parser) matchKeyword(kw string) bool {
	tok := p.peek()
	if (tok.Type == tokKeyword || tok.Type == tokNull || tok.Type == tokBool) && strings.EqualFold(tok.Value, kw) {
		p.next()
		return true
	}
	return false
}

func (p *Parser) expect(expectedType TokenType, val string) (Token, error) {
	tok := p.next()
	if tok.Type != expectedType {
		return tok, fmt.Errorf("expected token type %v, got %v (%s)", expectedType, tok.Type, tok.Value)
	}
	if val != "" && !strings.EqualFold(tok.Value, val) {
		return tok, fmt.Errorf("expected %q, got %q", val, tok.Value)
	}
	return tok, nil
}

// parseSelect parses SELECT ... FROM ... [WHERE ...] [GROUP BY ...] [ORDER BY ...] [LIMIT ...]
func (p *Parser) parseSelect() (*SelectStmt, error) {
	p.next() // consume SELECT

	stmt := &SelectStmt{Limit: -1}

	if p.matchKeyword("DISTINCT") {
		stmt.Distinct = true
	}

	// Parse Select Items
	for {
		item, err := p.parseSelectItem()
		if err != nil {
			return nil, err
		}
		stmt.Items = append(stmt.Items, item)

		if p.peek().Type == tokComma {
			p.next()
			continue
		}
		break
	}

	// FROM table
	if !p.matchKeyword("FROM") {
		return nil, fmt.Errorf("expected FROM clause in SELECT statement")
	}

	tblTok := p.next()
	if tblTok.Type != tokIdent && tblTok.Type != tokKeyword {
		return nil, fmt.Errorf("expected table name after FROM, got %s", tblTok.Value)
	}
	stmt.Table = tblTok.Value

	// WHERE clause
	if p.matchKeyword("WHERE") {
		whereExpr, err := p.parseExpression()
		if err != nil {
			return nil, fmt.Errorf("error in WHERE clause: %w", err)
		}
		stmt.Where = whereExpr
	}

	// GROUP BY
	if p.matchKeyword("GROUP") {
		if !p.matchKeyword("BY") {
			return nil, fmt.Errorf("expected BY after GROUP")
		}
		for {
			col := p.next().Value
			stmt.GroupBy = append(stmt.GroupBy, col)
			if p.peek().Type == tokComma {
				p.next()
				continue
			}
			break
		}
	}

	// ORDER BY
	if p.matchKeyword("ORDER") {
		if !p.matchKeyword("BY") {
			return nil, fmt.Errorf("expected BY after ORDER")
		}
		for {
			col := p.next().Value
			desc := false
			if p.matchKeyword("DESC") {
				desc = true
			} else if p.matchKeyword("ASC") {
				desc = false
			}
			stmt.OrderBy = append(stmt.OrderBy, OrderItem{Column: col, Desc: desc})
			if p.peek().Type == tokComma {
				p.next()
				continue
			}
			break
		}
	}

	// LIMIT
	if p.matchKeyword("LIMIT") {
		numTok := p.next()
		limit, err := strconv.Atoi(numTok.Value)
		if err != nil {
			return nil, fmt.Errorf("invalid LIMIT number: %s", numTok.Value)
		}
		stmt.Limit = limit

		if p.matchKeyword("OFFSET") {
			offTok := p.next()
			offset, err := strconv.Atoi(offTok.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid OFFSET number: %s", offTok.Value)
			}
			stmt.Offset = offset
		}
	}

	// Consume optional trailing semicolon
	if p.peek().Type == tokSemicolon {
		p.next()
	}

	return stmt, nil
}

func (p *Parser) parseSelectItem() (SelectItem, error) {
	if p.peek().Type == tokStar {
		p.next()
		return SelectItem{Expr: ColRefExpr{Name: "*"}}, nil
	}

	expr, err := p.parseExpression()
	if err != nil {
		return SelectItem{}, err
	}

	alias := ""
	if p.matchKeyword("AS") {
		tok := p.next()
		alias = tok.Value
	} else if p.peek().Type == tokIdent {
		tok := p.next()
		alias = tok.Value
	}

	return SelectItem{Expr: expr, Alias: alias}, nil
}

// Expression Parser with Precedence (OR -> AND -> NOT -> Comparisons -> Terms -> Factors)
func (p *Parser) parseExpression() (Expr, error) {
	return p.parseOr()
}

func (p *Parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.matchKeyword("OR") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = BinaryExpr{Op: "OR", Left: left, Right: right}
	}
	return left, nil
}

func (p *Parser) parseAnd() (Expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.matchKeyword("AND") {
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = BinaryExpr{Op: "AND", Left: left, Right: right}
	}
	return left, nil
}

func (p *Parser) parseNot() (Expr, error) {
	if p.matchKeyword("NOT") {
		sub, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return UnaryExpr{Op: "NOT", Expr: sub}, nil
	}
	return p.parseComparison()
}

func (p *Parser) parseComparison() (Expr, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}

	tok := p.peek()
	if tok.Type == tokOp && (tok.Value == "=" || tok.Value == "!=" || tok.Value == "<>" ||
		tok.Value == "<" || tok.Value == "<=" || tok.Value == ">" || tok.Value == ">=") {
		p.next()
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		return BinaryExpr{Op: tok.Value, Left: left, Right: right}, nil
	}

	// IS [NOT] NULL
	if p.matchKeyword("IS") {
		isNot := p.matchKeyword("NOT")
		if !p.matchKeyword("NULL") {
			return nil, fmt.Errorf("expected NULL after IS")
		}
		return IsNullExpr{Expr: left, Not: isNot}, nil
	}

	// [NOT] LIKE 'pattern'
	notLike := p.matchKeyword("NOT")
	if p.matchKeyword("LIKE") {
		pattern, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		return LikeExpr{Expr: left, Pattern: pattern, Not: notLike}, nil
	}
	if notLike {
		return nil, fmt.Errorf("unexpected NOT without LIKE or IN")
	}

	// [NOT] IN (v1, v2, ...)
	if p.matchKeyword("IN") {
		if _, err := p.expect(tokLParen, "("); err != nil {
			return nil, err
		}
		var values []Expr
		for {
			val, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			values = append(values, val)
			if p.peek().Type == tokComma {
				p.next()
				continue
			}
			break
		}
		if _, err := p.expect(tokRParen, ")"); err != nil {
			return nil, err
		}
		return InExpr{Expr: left, Values: values, Not: false}, nil
	}

	return left, nil
}

func (p *Parser) parseAdditive() (Expr, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.Type == tokOp && (tok.Value == "+" || tok.Value == "-") {
			p.next()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			left = BinaryExpr{Op: tok.Value, Left: left, Right: right}
			continue
		}
		break
	}
	return left, nil
}

func (p *Parser) parsePrimary() (Expr, error) {
	tok := p.peek()

	// Parenthesized sub-expression
	if tok.Type == tokLParen {
		p.next()
		expr, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRParen, ")"); err != nil {
			return nil, err
		}
		return expr, nil
	}

	// Literal String
	if tok.Type == tokString {
		p.next()
		return LiteralExpr{Value: tok.Value}, nil
	}

	// Literal Number
	if tok.Type == tokNumber {
		p.next()
		if strings.Contains(tok.Value, ".") {
			flt, _ := strconv.ParseFloat(tok.Value, 64)
			return LiteralExpr{Value: flt}, nil
		}
		num, _ := strconv.ParseInt(tok.Value, 10, 64)
		return LiteralExpr{Value: num}, nil
	}

	// Literal Bool
	if tok.Type == tokBool {
		p.next()
		return LiteralExpr{Value: strings.EqualFold(tok.Value, "TRUE")}, nil
	}

	// Literal Null
	if tok.Type == tokNull {
		p.next()
		return LiteralExpr{Value: nil}, nil
	}

	// Aggregate Functions (COUNT, SUM, AVG, MIN, MAX)
	if tok.Type == tokKeyword && (tok.Value == "COUNT" || tok.Value == "SUM" ||
		tok.Value == "AVG" || tok.Value == "MIN" || tok.Value == "MAX") {
		fnName := p.next().Value
		if _, err := p.expect(tokLParen, "("); err != nil {
			return nil, err
		}
		var arg Expr
		if p.peek().Type == tokStar {
			p.next()
			arg = ColRefExpr{Name: "*"}
		} else {
			var err error
			arg, err = p.parsePrimary()
			if err != nil {
				return nil, err
			}
		}
		if _, err := p.expect(tokRParen, ")"); err != nil {
			return nil, err
		}
		return FuncCallExpr{Name: fnName, Arg: arg}, nil
	}

	// Star
	if tok.Type == tokStar {
		p.next()
		return ColRefExpr{Name: "*"}, nil
	}

	// Column Identifier
	if tok.Type == tokIdent || tok.Type == tokKeyword {
		p.next()
		return ColRefExpr{Name: tok.Value}, nil
	}

	return nil, fmt.Errorf("unexpected token in expression: %v (%s)", tok.Type, tok.Value)
}

// ── DML / DDL Parsers ─────────────────────────────────────────────────────────

func (p *Parser) parseInsert() (*InsertStmt, error) {
	p.next() // consume INSERT
	if !p.matchKeyword("INTO") {
		return nil, fmt.Errorf("expected INTO after INSERT")
	}

	tbl := p.next().Value
	stmt := &InsertStmt{Table: tbl}

	// Optional column list: (c1, c2)
	if p.peek().Type == tokLParen {
		p.next()
		for {
			col := p.next().Value
			stmt.Columns = append(stmt.Columns, col)
			if p.peek().Type == tokComma {
				p.next()
				continue
			}
			break
		}
		if _, err := p.expect(tokRParen, ")"); err != nil {
			return nil, err
		}
	}

	if !p.matchKeyword("VALUES") {
		return nil, fmt.Errorf("expected VALUES in INSERT statement")
	}

	// Multiple tuples: (v1, v2), (v3, v4)
	for {
		if _, err := p.expect(tokLParen, "("); err != nil {
			return nil, err
		}
		var row []Expr
		for {
			val, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			row = append(row, val)
			if p.peek().Type == tokComma {
				p.next()
				continue
			}
			break
		}
		if _, err := p.expect(tokRParen, ")"); err != nil {
			return nil, err
		}
		stmt.Values = append(stmt.Values, row)

		if p.peek().Type == tokComma {
			p.next()
			continue
		}
		break
	}

	return stmt, nil
}

func (p *Parser) parseUpdate() (*UpdateStmt, error) {
	p.next() // consume UPDATE
	tbl := p.next().Value
	stmt := &UpdateStmt{Table: tbl, Assignments: make(map[string]Expr)}

	if !p.matchKeyword("SET") {
		return nil, fmt.Errorf("expected SET after UPDATE table")
	}

	for {
		col := p.next().Value
		if _, err := p.expect(tokOp, "="); err != nil {
			return nil, err
		}
		expr, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		stmt.Assignments[col] = expr

		if p.peek().Type == tokComma {
			p.next()
			continue
		}
		break
	}

	if p.matchKeyword("WHERE") {
		whereExpr, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		stmt.Where = whereExpr
	}

	return stmt, nil
}

func (p *Parser) parseDelete() (*DeleteStmt, error) {
	p.next() // consume DELETE
	if !p.matchKeyword("FROM") {
		return nil, fmt.Errorf("expected FROM after DELETE")
	}

	tbl := p.next().Value
	stmt := &DeleteStmt{Table: tbl}

	if p.matchKeyword("WHERE") {
		whereExpr, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		stmt.Where = whereExpr
	}

	return stmt, nil
}

func (p *Parser) parseCreateTable() (*CreateTableStmt, error) {
	p.next() // consume CREATE
	if !p.matchKeyword("TABLE") {
		return nil, fmt.Errorf("expected TABLE after CREATE")
	}

	tbl := p.next().Value
	stmt := &CreateTableStmt{Table: tbl}

	if _, err := p.expect(tokLParen, "("); err != nil {
		return nil, err
	}

	for {
		colName := p.next().Value
		colType := p.next().Value
		stmt.Columns = append(stmt.Columns, Column{Name: colName, Type: strings.ToUpper(colType)})

		if p.peek().Type == tokComma {
			p.next()
			continue
		}
		break
	}

	if _, err := p.expect(tokRParen, ")"); err != nil {
		return nil, err
	}

	return stmt, nil
}

func (p *Parser) parseDropTable() (*DropTableStmt, error) {
	p.next() // consume DROP
	if !p.matchKeyword("TABLE") {
		return nil, fmt.Errorf("expected TABLE after DROP")
	}
	tbl := p.next().Value
	return &DropTableStmt{Table: tbl}, nil
}
