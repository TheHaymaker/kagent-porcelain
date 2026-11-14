package bigquery

import (
	"fmt"
	"strings"
	"unicode"
)

// forbidden are keywords that only appear in DML, DDL, DCL, scripting or
// data-movement statements. A read-only query must contain none of them
// outside string literals, comments and backtick-quoted identifiers.
var forbidden = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "TRUNCATE": true,
	"CREATE": true, "DROP": true, "ALTER": true, "UNDROP": true, "RENAME": true,
	"GRANT": true, "REVOKE": true,
	"EXPORT": true, "LOAD": true, "COPY": true,
	"CALL": true, "EXECUTE": true, "IMMEDIATE": true, "DECLARE": true, "SET": true,
	"BEGIN": true, "COMMIT": true, "ROLLBACK": true, "TRANSACTION": true,
	"ASSERT": true, "RAISE": true, "LOOP": true, "WHILE": true,
}

// CheckReadOnly is the first, lexical layer of DML/DDL rejection for
// bigquery_run_query: a single statement starting with SELECT or WITH and no
// statement-level keywords. The second, authoritative layer is the dry run's
// statementType, which must be SELECT.
func CheckReadOnly(sql string) error {
	words, err := tokenize(sql)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return fmt.Errorf("query is empty")
	}
	first := words[0]
	if first != "SELECT" && first != "WITH" && first != "(" {
		return fmt.Errorf("only SELECT queries are allowed (statement starts with %s)", first)
	}
	for i, w := range words {
		if w == ";" {
			if i != len(words)-1 {
				return fmt.Errorf("only a single statement is allowed")
			}
			continue
		}
		if forbidden[w] {
			return fmt.Errorf("only read-only SELECT queries are allowed; %s is not permitted (quote identifiers with backticks if %s is a column name)", w, strings.ToLower(w))
		}
	}
	return nil
}

// tokenize returns upper-cased bare words plus "(" (only as the first token)
// and ";" markers, skipping comments, string/bytes literals and quoted
// identifiers.
func tokenize(s string) ([]string, error) {
	var words []string
	r := []rune(s)
	n := len(r)
	for i := 0; i < n; {
		c := r[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '-' && i+1 < n && r[i+1] == '-', c == '#':
			for i < n && r[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && r[i+1] == '*':
			j := i + 2
			for j+1 < n && !(r[j] == '*' && r[j+1] == '/') {
				j++
			}
			if j+1 >= n {
				return nil, fmt.Errorf("unterminated block comment")
			}
			i = j + 2
		case c == '\'' || c == '"':
			j, err := skipString(r, i)
			if err != nil {
				return nil, err
			}
			i = j
		case c == '`':
			j := i + 1
			for j < n && r[j] != '`' {
				if r[j] == '\\' {
					j++
				}
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("unterminated quoted identifier")
			}
			i = j + 1
		case c == ';':
			words = append(words, ";")
			i++
		case c == '(':
			if len(words) == 0 {
				words = append(words, "(")
			}
			i++
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < n && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_') {
				j++
			}
			word := strings.ToUpper(string(r[i:j]))
			// String prefixes: r'..', b'..', rb'..', br'..'.
			if j < n && (r[j] == '\'' || r[j] == '"') && (word == "R" || word == "B" || word == "RB" || word == "BR") {
				k, err := skipString(r, j)
				if err != nil {
					return nil, err
				}
				i = k
				continue
			}
			words = append(words, word)
			i = j
		default:
			i++
		}
	}
	return words, nil
}

// skipString returns the index after the string literal starting at r[i],
// handling triple quotes and backslash escapes.
func skipString(r []rune, i int) (int, error) {
	q := r[i]
	n := len(r)
	if i+2 < n && r[i+1] == q && r[i+2] == q {
		for j := i + 3; j+2 < n; j++ {
			if r[j] == '\\' {
				j++
				continue
			}
			if r[j] == q && r[j+1] == q && r[j+2] == q {
				return j + 3, nil
			}
		}
		return 0, fmt.Errorf("unterminated string literal")
	}
	for j := i + 1; j < n; j++ {
		if r[j] == '\\' {
			j++
			continue
		}
		if r[j] == q {
			return j + 1, nil
		}
	}
	return 0, fmt.Errorf("unterminated string literal")
}
