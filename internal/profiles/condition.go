package profiles

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// EvalCondition evaluates an Orca/PrusaSlicer compatible_printers_condition
// expression against a resolved machine preset.
//
// Supported: identifiers, identifier[index], numbers, "strings", /regex/,
// == != < > <= >= =~ !~, and/or/not, && || !, parentheses, true/false.
// That covers the expressions shipped in Orca's system profiles.
func EvalCondition(expr string, vars map[string]any) (bool, error) {
	toks, err := lex(expr)
	if err != nil {
		return false, err
	}
	p := &cparser{toks: toks, vars: vars}
	v, err := p.or()
	if err != nil {
		return false, err
	}
	if p.pos != len(p.toks) {
		return false, fmt.Errorf("unexpected %q", p.toks[p.pos].s)
	}
	return truthy(v), nil
}

type tkind int

const (
	tIdent tkind = iota
	tNum
	tStr
	tRegex
	tOp
	tLParen
	tRParen
	tLBrack
	tRBrack
)

type tok struct {
	k tkind
	s string
}

func lex(s string) ([]tok, error) {
	var out []tok
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			out = append(out, tok{tLParen, "("})
			i++
		case c == ')':
			out = append(out, tok{tRParen, ")"})
			i++
		case c == '[':
			out = append(out, tok{tLBrack, "["})
			i++
		case c == ']':
			out = append(out, tok{tRBrack, "]"})
			i++
		case c == '"':
			j := i + 1
			var b strings.Builder
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				b.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string")
			}
			out = append(out, tok{tStr, b.String()})
			i = j + 1
		case c == '/':
			// A regex can only follow =~ or !~.
			if n := len(out); n == 0 || out[n-1].k != tOp || (out[n-1].s != "=~" && out[n-1].s != "!~") {
				return nil, fmt.Errorf("unexpected '/'")
			}
			j := i + 1
			for j < len(s) && s[j] != '/' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated regex")
			}
			out = append(out, tok{tRegex, s[i+1 : j]})
			i = j + 1
		case strings.ContainsRune("=!<>&|", rune(c)):
			two := ""
			if i+1 < len(s) {
				two = s[i : i+2]
			}
			switch two {
			case "==", "!=", "<=", ">=", "=~", "!~", "&&", "||":
				out = append(out, tok{tOp, two})
				i += 2
			default:
				if c == '=' || c == '&' || c == '|' {
					return nil, fmt.Errorf("unexpected %q", string(c))
				}
				out = append(out, tok{tOp, string(c)})
				i++
			}
		case c == '-' || c == '.' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(s) && (s[j] == '.' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			out = append(out, tok{tNum, s[i:j]})
			i = j
		case c == '_' || unicode.IsLetter(rune(c)):
			j := i + 1
			for j < len(s) && (s[j] == '_' || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j]))) {
				j++
			}
			w := s[i:j]
			switch w {
			case "and", "or", "not":
				out = append(out, tok{tOp, w})
			default:
				out = append(out, tok{tIdent, w})
			}
			i = j
		default:
			return nil, fmt.Errorf("unexpected %q", string(c))
		}
	}
	return out, nil
}

type cparser struct {
	toks []tok
	pos  int
	vars map[string]any
}

func (p *cparser) peekOp(ops ...string) (string, bool) {
	if p.pos >= len(p.toks) || p.toks[p.pos].k != tOp {
		return "", false
	}
	for _, o := range ops {
		if p.toks[p.pos].s == o {
			return o, true
		}
	}
	return "", false
}

func (p *cparser) or() (any, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.peekOp("or", "||"); !ok {
			return l, nil
		}
		p.pos++
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = truthy(l) || truthy(r)
	}
}

func (p *cparser) and() (any, error) {
	l, err := p.not()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.peekOp("and", "&&"); !ok {
			return l, nil
		}
		p.pos++
		r, err := p.not()
		if err != nil {
			return nil, err
		}
		l = truthy(l) && truthy(r)
	}
}

func (p *cparser) not() (any, error) {
	if _, ok := p.peekOp("not", "!"); ok {
		p.pos++
		v, err := p.not()
		if err != nil {
			return nil, err
		}
		return !truthy(v), nil
	}
	return p.cmp()
}

func (p *cparser) cmp() (any, error) {
	l, err := p.primary()
	if err != nil {
		return nil, err
	}
	op, ok := p.peekOp("==", "!=", "<", ">", "<=", ">=", "=~", "!~")
	if !ok {
		return l, nil
	}
	p.pos++
	if op == "=~" || op == "!~" {
		if p.pos >= len(p.toks) || p.toks[p.pos].k != tRegex {
			return nil, fmt.Errorf("expected regex after %s", op)
		}
		re, err := regexp.Compile(p.toks[p.pos].s)
		if err != nil {
			return nil, err
		}
		p.pos++
		m := re.MatchString(fmt.Sprint(l))
		if op == "!~" {
			m = !m
		}
		return m, nil
	}
	r, err := p.primary()
	if err != nil {
		return nil, err
	}
	lf, lnum := num(l)
	rf, rnum := num(r)
	if lnum && rnum {
		switch op {
		case "==":
			return lf == rf, nil
		case "!=":
			return lf != rf, nil
		case "<":
			return lf < rf, nil
		case ">":
			return lf > rf, nil
		case "<=":
			return lf <= rf, nil
		case ">=":
			return lf >= rf, nil
		}
	}
	ls, rs := fmt.Sprint(l), fmt.Sprint(r)
	switch op {
	case "==":
		return ls == rs, nil
	case "!=":
		return ls != rs, nil
	case "<":
		return ls < rs, nil
	case ">":
		return ls > rs, nil
	case "<=":
		return ls <= rs, nil
	default:
		return ls >= rs, nil
	}
}

func (p *cparser) primary() (any, error) {
	if p.pos >= len(p.toks) {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	t := p.toks[p.pos]
	p.pos++
	switch t.k {
	case tLParen:
		v, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.pos >= len(p.toks) || p.toks[p.pos].k != tRParen {
			return nil, fmt.Errorf("missing )")
		}
		p.pos++
		return v, nil
	case tNum:
		f, err := strconv.ParseFloat(t.s, 64)
		if err != nil {
			return nil, err
		}
		return f, nil
	case tStr:
		return t.s, nil
	case tIdent:
		if t.s == "true" {
			return true, nil
		}
		if t.s == "false" {
			return false, nil
		}
		idx := 0
		if p.pos < len(p.toks) && p.toks[p.pos].k == tLBrack {
			if p.pos+2 >= len(p.toks) || p.toks[p.pos+1].k != tNum || p.toks[p.pos+2].k != tRBrack {
				return nil, fmt.Errorf("bad index on %s", t.s)
			}
			idx, _ = strconv.Atoi(p.toks[p.pos+1].s)
			p.pos += 3
		}
		v, ok := p.vars[t.s]
		if !ok {
			return "", nil
		}
		list := strs(v)
		if len(list) == 0 {
			return "", nil
		}
		if idx >= len(list) {
			idx = len(list) - 1
		}
		s := list[idx]
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, nil
		}
		return s, nil
	}
	return nil, fmt.Errorf("unexpected %q", t.s)
}

func num(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != "" && t != "0" && t != "false"
	}
	return false
}
