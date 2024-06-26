package main

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/oarkflow/filters"
)

type TokenType int

const (
	TokenEOF TokenType = iota
	TokenSelect
	TokenFrom
	TokenWhere
	TokenAnd
	TokenOr
	TokenIs
	TokenNot
	TokenNull
	TokenBetween
	TokenLike
	TokenIdent
	TokenOperator
	TokenValue
	TokenLParen
	TokenRParen
	TokenComma
	TokenStar
)

type Token struct {
	Type  TokenType
	Value string
}

func isWhitespace(ch rune) bool {
	return unicode.IsSpace(ch)
}

func isLetter(ch rune) bool {
	return unicode.IsLetter(ch)
}

func isDigit(ch rune) bool {
	return unicode.IsDigit(ch)
}

func isIdentChar(ch rune) bool {
	return isLetter(ch) || isDigit(ch) || ch == '_'
}

func tokenize(input string) ([]Token, error) {
	var tokens []Token
	i := 0
	length := len(input)
	for i < length {
		ch := rune(input[i])
		if isWhitespace(ch) {
			i++
			continue
		}
		if isLetter(ch) {
			start := i
			for i < length && isIdentChar(rune(input[i])) {
				i++
			}
			word := input[start:i]
			switch strings.ToUpper(word) {
			case "SELECT":
				tokens = append(tokens, Token{Type: TokenSelect, Value: word})
			case "FROM":
				tokens = append(tokens, Token{Type: TokenFrom, Value: word})
			case "WHERE":
				tokens = append(tokens, Token{Type: TokenWhere, Value: word})
			case "AND":
				tokens = append(tokens, Token{Type: TokenAnd, Value: word})
			case "OR":
				tokens = append(tokens, Token{Type: TokenOr, Value: word})
			case "IS":
				tokens = append(tokens, Token{Type: TokenIs, Value: word})
			case "NOT":
				tokens = append(tokens, Token{Type: TokenNot, Value: word})
			case "NULL":
				tokens = append(tokens, Token{Type: TokenNull, Value: word})
			case "BETWEEN":
				tokens = append(tokens, Token{Type: TokenBetween, Value: word})
			case "LIKE":
				tokens = append(tokens, Token{Type: TokenLike, Value: word})
			default:
				tokens = append(tokens, Token{Type: TokenIdent, Value: word})
			}
			continue
		}
		if isDigit(ch) || ch == '\'' || ch == '"' {
			start := i
			if ch == '\'' || ch == '"' {
				i++
				for i < length && rune(input[i]) != ch {
					i++
				}
				i++ // skip closing quote
			} else {
				for i < length && isDigit(rune(input[i])) {
					i++
				}
			}
			tokens = append(tokens, Token{Type: TokenValue, Value: input[start:i]})
			continue
		}
		switch ch {
		case '>', '<', '=', '!':
			start := i
			if i+1 < length && input[i+1] == '=' {
				i++
			}
			tokens = append(tokens, Token{Type: TokenOperator, Value: input[start : i+1]})
			i++
		case '(', ')':
			if ch == '(' {
				tokens = append(tokens, Token{Type: TokenLParen, Value: string(ch)})
			} else {
				tokens = append(tokens, Token{Type: TokenRParen, Value: string(ch)})
			}
			i++
		case ',':
			tokens = append(tokens, Token{Type: TokenComma, Value: string(ch)})
			i++
		case '*':
			tokens = append(tokens, Token{Type: TokenStar, Value: string(ch)})
			i++
		default:
			return nil, fmt.Errorf("unrecognized character: %c", ch)
		}
	}
	tokens = append(tokens, Token{Type: TokenEOF, Value: ""})
	return tokens, nil
}

type ASTNodeType int

const (
	NodeSelect ASTNodeType = iota
	NodeFrom
	NodeWhere
	NodeCondition
	NodeAnd
	NodeOr
)

type Condition struct {
	Left     string
	Operator string
	Right    []string
}

type ASTNode struct {
	Type      ASTNodeType
	Value     string
	Children  []*ASTNode
	Condition *Condition
}

type Parser struct {
	tokens []Token
	pos    int
}

func NewParser(tokens []Token) *Parser {
	return &Parser{tokens: tokens}
}

func (p *Parser) parse() (*ASTNode, error) {
	if p.tokens[p.pos].Type == TokenSelect {
		selectNode := &ASTNode{Type: NodeSelect, Value: "SELECT"}
		p.pos++
		for p.tokens[p.pos].Type == TokenIdent || p.tokens[p.pos].Type == TokenStar {
			selectNode.Children = append(selectNode.Children, &ASTNode{Type: NodeCondition, Value: p.tokens[p.pos].Value})
			p.pos++
			if p.tokens[p.pos].Type == TokenComma {
				p.pos++
			}
		}
		if p.tokens[p.pos].Type != TokenFrom {
			return nil, fmt.Errorf("expected FROM, got %s", p.tokens[p.pos].Value)
		}
		fromNode := &ASTNode{Type: NodeFrom, Value: "FROM"}
		p.pos++
		if p.tokens[p.pos].Type != TokenIdent {
			return nil, fmt.Errorf("expected table name, got %s", p.tokens[p.pos].Value)
		}
		fromNode.Children = append(fromNode.Children, &ASTNode{Type: NodeCondition, Value: p.tokens[p.pos].Value})
		selectNode.Children = append(selectNode.Children, fromNode)
		p.pos++
		if p.tokens[p.pos].Type == TokenWhere {
			whereNode := &ASTNode{Type: NodeWhere, Value: "WHERE"}
			p.pos++
			conditionNode, err := p.parseCondition()
			if err != nil {
				return nil, err
			}
			whereNode.Children = append(whereNode.Children, conditionNode)
			selectNode.Children = append(selectNode.Children, whereNode)
		}
		return selectNode, nil
	} else {
		return p.parseCondition()
	}
}

func (p *Parser) parseCondition() (*ASTNode, error) {
	conditionNode, err := p.parsePrimaryCondition()
	if err != nil {
		return nil, err
	}

	for p.tokens[p.pos].Type == TokenAnd || p.tokens[p.pos].Type == TokenOr {
		operator := p.tokens[p.pos]
		p.pos++
		rightCondition, err := p.parsePrimaryCondition()
		if err != nil {
			return nil, err
		}

		// If the next token is the same operator, keep appending to the same node
		for p.tokens[p.pos].Type == operator.Type {
			p.pos++
			nextCondition, err := p.parsePrimaryCondition()
			if err != nil {
				return nil, err
			}
			rightCondition = &ASTNode{Type: operatorTypeToNodeType(operator.Type), Value: operator.Value, Children: []*ASTNode{rightCondition, nextCondition}}
		}

		conditionNode = &ASTNode{Type: operatorTypeToNodeType(operator.Type), Value: operator.Value, Children: []*ASTNode{conditionNode, rightCondition}}
	}

	return conditionNode, nil
}

func operatorTypeToNodeType(operatorType TokenType) ASTNodeType {
	switch operatorType {
	case TokenAnd:
		return NodeAnd
	case TokenOr:
		return NodeOr
	default:
		panic("unsupported operator type")
	}
}

func (p *Parser) parsePrimaryCondition() (*ASTNode, error) {
	if p.tokens[p.pos].Type == TokenLParen {
		p.pos++
		node, err := p.parseCondition()
		if err != nil {
			return nil, err
		}
		if p.tokens[p.pos].Type != TokenRParen {
			return nil, fmt.Errorf("expected closing parenthesis, got %s", p.tokens[p.pos].Value)
		}
		p.pos++
		return node, nil
	}

	left := p.tokens[p.pos]
	if left.Type != TokenIdent {
		return nil, fmt.Errorf("expected identifier, got %s", left.Value)
	}
	p.pos++

	operator := p.tokens[p.pos]
	if operator.Type != TokenOperator && operator.Type != TokenIs && operator.Type != TokenBetween && operator.Type != TokenLike {
		return nil, fmt.Errorf("expected operator, got %s", operator.Value)
	}
	p.pos++

	var rightValues []string
	if operator.Type == TokenIs {
		if p.tokens[p.pos].Type == TokenNot {
			operator.Value += " NOT"
			p.pos++
		}
		if p.tokens[p.pos].Type != TokenNull {
			return nil, fmt.Errorf("expected NULL, got %s", p.tokens[p.pos].Value)
		}
		rightValues = append(rightValues, p.tokens[p.pos].Value)
		p.pos++
	} else if operator.Type == TokenBetween {
		rightValues = append(rightValues, p.tokens[p.pos].Value)
		p.pos++
		if p.tokens[p.pos].Type != TokenAnd {
			return nil, fmt.Errorf("expected AND, got %s", p.tokens[p.pos].Value)
		}
		p.pos++
		rightValues = append(rightValues, p.tokens[p.pos].Value)
		p.pos++
	} else {
		rightValues = append(rightValues, p.tokens[p.pos].Value)
		p.pos++
	}
	return &ASTNode{
		Type: NodeCondition,
		Condition: &Condition{
			Left:     left.Value,
			Operator: operator.Value,
			Right:    rightValues,
		},
	}, nil
}

func validate(node *ASTNode, data map[string]interface{}) bool {
	switch node.Type {
	case NodeCondition:
		return evaluateCondition(node.Condition, data)
	case NodeAnd:
		for _, child := range node.Children {
			if !validate(child, data) {
				return false
			}
		}
		return true
	case NodeOr:
		for _, child := range node.Children {
			fmt.Println(child.Condition, child.Type)
			if validate(child, data) {
				return true
			}
		}
		return false
	default:
		for _, child := range node.Children {
			if child.Condition == nil {
				continue
			}

			if !validate(child, data) {
				return false
			}
		}
		return true
	}
}

func toOperator(sqlOperator string) filters.Operator {
	switch strings.ToUpper(sqlOperator) {
	case "=":
		return filters.Equal
	case "!=", "<>":
		return filters.NotEqual
	case ">":
		return filters.GreaterThan
	case "<":
		return filters.LessThan
	case ">=":
		return filters.GreaterThanEqual
	case "<=":
		return filters.LessThanEqual
	case "LIKE":
		return filters.Contains
	case "NOT LIKE":
		return filters.NotContains
	case "BETWEEN":
		return filters.Between
	case "IN":
		return filters.In
	case "IS NULL":
		return filters.IsNull
	case "IS NOT NULL":
		return filters.NotNull
	default:
		return ""
	}
}

func evaluateCondition(cond *Condition, data map[string]interface{}) bool {
	value, ok := data[cond.Left]
	if !ok {
		return false
	}
	if strings.Contains(cond.Operator, "IS") {
		cond.Operator += " " + cond.Right[0]
	}
	switch cond.Operator {
	case ">":
		return value.(int) > parseInt(cond.Right[0])
	case "<":
		return value.(int) < parseInt(cond.Right[0])
	case "=":
		return value == parseValue(cond.Right[0])
	case "IS NOT NULL":
		return value != nil
	case "IS NULL":
		return value == nil
	case "LIKE":
		val := strings.Trim(cond.Right[0], "'")
		if strings.HasPrefix(val, "%") && strings.HasSuffix(val, "%") {
			val = strings.Trim(val, "%")
			return strings.Contains(value.(string), val)
		} else if strings.HasPrefix(val, "%") && !strings.HasSuffix(val, "%") {
			val = strings.Trim(val, "%")
			return strings.HasSuffix(value.(string), val)
		} else if !strings.HasPrefix(val, "%") && strings.HasSuffix(val, "%") {
			val = strings.Trim(val, "%")
			return strings.HasPrefix(value.(string), val)
		}
		return value.(string) == val
	case "BETWEEN":
		return value.(int) >= parseInt(cond.Right[0]) && value.(int) <= parseInt(cond.Right[1])
	default:
		return false
	}
}

func parseInt(s string) int {
	var i int
	fmt.Sscanf(s, "%d", &i)
	return i
}

func parseValue(s string) interface{} {
	if s[0] == '\'' {
		return strings.Trim(s, "'")
	}
	return parseInt(s)
}

func printAST(node *ASTNode, indent int) {
	for i := 0; i < indent; i++ {
		fmt.Print(" ")
	}
	if node.Condition != nil {
		fmt.Printf("%s %s %v\n", node.Condition.Left, node.Condition.Operator, node.Condition.Right)
	} else {
		fmt.Println(node.Value)
	}
	for _, child := range node.Children {
		printAST(child, indent+1)
	}
}

func main() {
	sql := "(age > 21 AND name = 'John Doe') AND (age IS NOT NULL AND name LIKE 'John%' AND (age BETWEEN 12 AND 200))"
	tokens, err := tokenize(sql)
	if err != nil {
		fmt.Println("Error tokenizing SQL:", err)
		return
	}

	parser := NewParser(tokens)
	ast, err := parser.parse()
	if err != nil {
		fmt.Println("Error parsing SQL:", err)
		return
	}

	/*fmt.Println("Parsed AST:")
	printAST(ast, 0)*/

	data := map[string]interface{}{
		"age":  25,
		"name": "John Doe",
	}

	fmt.Println("Validation result:", validate(ast, data))
}
