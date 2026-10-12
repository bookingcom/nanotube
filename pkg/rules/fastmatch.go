package rules

import (
	"bytes"
	"regexp"
	"regexp/syntax"
	"unicode/utf8"
)

// matcher answers re.Match(s) exactly, but avoids running the regexp engine
// where the structure of the expression allows it.
//
// Two strategies are used, both derived from the parsed expression:
//
//   - glob: expressions built only from ASCII literals, unbounded wildcards
//     (`.*`, `.+`) and the `^`/`$` anchors are matched with prefix/suffix
//     checks and bytes.Index. Inputs containing '\n' are deferred to the
//     regexp, since `.` does not match it.
//   - prefilter: for any other expression, an anchored literal prefix and the
//     literals every match must contain are checked first. They are necessary
//     conditions only; when they hold the regexp still decides.
type matcher struct {
	re *regexp.Regexp

	glob []globTok // nil when the expression is not glob-shaped

	prefix   []byte
	required [][]byte
}

// globTok is either a literal (lit != nil) or a wildcard matching at least
// min (0 or 1) and arbitrarily many characters.
type globTok struct {
	lit []byte
	min int
}

func newMatcher(re *regexp.Regexp) matcher {
	m := matcher{re: re}

	// regexp.Compile parses with syntax.Perl, so the analysed tree has the
	// same semantics as the compiled program.
	ast, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return m
	}
	ast = ast.Simplify()

	m.glob = globTokens(ast)
	if m.glob == nil {
		m.prefix = anchoredPrefix(ast)
		m.required = requiredLiterals(ast, nil)
	}

	return m
}

// Match reports whether s contains a match of the regexp.
func (m *matcher) Match(s []byte) bool {
	if m.glob != nil {
		if bytes.IndexByte(s, '\n') >= 0 {
			return m.re.Match(s)
		}
		return globMatch(m.glob, s)
	}

	if !bytes.HasPrefix(s, m.prefix) {
		return false
	}
	for _, lit := range m.required {
		if !bytes.Contains(s, lit) {
			return false
		}
	}

	return m.re.Match(s)
}

// asciiLiteral returns the bytes of an OpLiteral node if it is
// case-sensitive and pure ASCII. ASCII bytes are always rune boundaries in
// Go's UTF-8 decoding, so byte-level search over them is exact.
func asciiLiteral(re *syntax.Regexp) ([]byte, bool) {
	if re.Op != syntax.OpLiteral || re.Flags&syntax.FoldCase != 0 {
		return nil, false
	}
	b := make([]byte, 0, len(re.Rune))
	for _, r := range re.Rune {
		if r >= utf8.RuneSelf {
			return nil, false
		}
		b = append(b, byte(r))
	}

	return b, true
}

func unwrapCapture(re *syntax.Regexp) *syntax.Regexp {
	for re.Op == syntax.OpCapture {
		re = re.Sub[0]
	}
	return re
}

// flatConcat returns the top-level sequence of the expression with captures
// unwrapped and nested concatenations flattened.
func flatConcat(re *syntax.Regexp) []*syntax.Regexp {
	re = unwrapCapture(re)
	if re.Op != syntax.OpConcat {
		return []*syntax.Regexp{re}
	}
	var items []*syntax.Regexp
	for _, sub := range re.Sub {
		items = append(items, flatConcat(sub)...)
	}
	return items
}

// globTokens converts a glob-shaped expression into an anchored token
// sequence, or returns nil if the expression is not glob-shaped. An
// unanchored start or end becomes a leading or trailing `.*`.
func globTokens(re *syntax.Regexp) []globTok {
	items := flatConcat(re)

	anchoredStart := len(items) > 0 && items[0].Op == syntax.OpBeginText
	if anchoredStart {
		items = items[1:]
	}
	anchoredEnd := len(items) > 0 && items[len(items)-1].Op == syntax.OpEndText
	if anchoredEnd {
		items = items[:len(items)-1]
	}

	toks := []globTok{}
	addWild := func(min int) {
		if n := len(toks); n > 0 && toks[n-1].lit == nil {
			toks[n-1].min += min
			return
		}
		toks = append(toks, globTok{min: min})
	}
	addLit := func(b []byte) {
		if n := len(toks); n > 0 && toks[n-1].lit != nil {
			toks[n-1].lit = append(toks[n-1].lit, b...)
			return
		}
		toks = append(toks, globTok{lit: b})
	}

	if !anchoredStart {
		addWild(0)
	}
	for _, it := range items {
		it = unwrapCapture(it)
		switch it.Op {
		case syntax.OpLiteral:
			b, ok := asciiLiteral(it)
			if !ok {
				return nil
			}
			addLit(b)
		case syntax.OpStar, syntax.OpPlus:
			sub := unwrapCapture(it.Sub[0])
			if sub.Op != syntax.OpAnyCharNotNL && sub.Op != syntax.OpAnyChar {
				return nil
			}
			if it.Op == syntax.OpPlus {
				addWild(1)
			} else {
				addWild(0)
			}
		case syntax.OpEmptyMatch:
		default:
			return nil
		}
	}
	if !anchoredEnd {
		addWild(0)
	}

	for _, t := range toks {
		// A wildcard requiring several characters counts runes, not bytes.
		if t.lit == nil && t.min > 1 {
			return nil
		}
	}

	return toks
}

// globMatch matches s against an anchored token sequence. Literals are placed
// at their leftmost feasible position; since every wildcard is unbounded
// above, an earlier placement never rules out a later one. s must not contain
// '\n'.
func globMatch(toks []globTok, s []byte) bool {
	pos := 0
	last := len(toks) - 1
	for i, t := range toks {
		if t.lit == nil {
			if i == last {
				return len(s)-pos >= t.min
			}
			continue
		}

		if i == 0 {
			if !bytes.HasPrefix(s, t.lit) {
				return false
			}
			pos = len(t.lit)
			continue
		}

		from := pos + toks[i-1].min
		if from > len(s) {
			return false
		}
		if i == last {
			start := len(s) - len(t.lit)
			return start >= from && bytes.Equal(s[start:], t.lit)
		}
		idx := bytes.Index(s[from:], t.lit)
		if idx < 0 {
			return false
		}
		pos = from + idx + len(t.lit)
	}

	return pos == len(s)
}

// anchoredPrefix returns the literal every match must start with at position
// 0, if the expression begins with `^` followed by literals.
func anchoredPrefix(re *syntax.Regexp) []byte {
	items := flatConcat(re)
	if len(items) == 0 || items[0].Op != syntax.OpBeginText {
		return nil
	}

	var prefix []byte
	for _, it := range items[1:] {
		b, ok := asciiLiteral(unwrapCapture(it))
		if !ok {
			break
		}
		prefix = append(prefix, b...)
	}

	return prefix
}

// requiredLiterals appends the literals that any match of re must contain.
func requiredLiterals(re *syntax.Regexp, acc [][]byte) [][]byte {
	switch re.Op {
	case syntax.OpLiteral:
		if b, ok := asciiLiteral(re); ok {
			acc = append(acc, b)
		}
	case syntax.OpCapture, syntax.OpPlus:
		acc = requiredLiterals(re.Sub[0], acc)
	case syntax.OpRepeat:
		if re.Min >= 1 {
			acc = requiredLiterals(re.Sub[0], acc)
		}
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			acc = requiredLiterals(sub, acc)
		}
	}

	return acc
}
