package intake

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
)

// SQL dumps: OLAP-Data-lakehouse's ExtractFromDump only recognized the
// INSERT lines. Here they are parsed: CREATE TABLE gives the columns,
// INSERT INTO ... VALUES (...), (...) and pg_dump's COPY ... FROM stdin
// give the rows. Good enough for MySQL and PostgreSQL dumps; anything else
// in the file is ignored.

var (
	createRe = regexp.MustCompile(`(?is)^\s*create\s+table\s+(?:if\s+not\s+exists\s+)?([^\s(]+)\s*\((.*)\)`)
	insertRe = regexp.MustCompile(`(?is)^\s*insert\s+(?:ignore\s+)?into\s+([^\s(]+)\s*(\(([^)]*)\))?\s*values\s*(.*)$`)
	copyRe   = regexp.MustCompile(`(?i)^\s*copy\s+([^\s(]+)\s*\(([^)]*)\)\s+from\s+stdin`)
	// Column definitions that are constraints, not columns.
	constraintRe = regexp.MustCompile(`(?i)^\s*(primary|unique|key|constraint|foreign|index|check|fulltext)\b`)
)

func ident(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return strings.Trim(s, "`\"[]")
}

func identList(s string) []string {
	var out []string
	for _, p := range splitTop(s, ',') {
		if c := ident(p); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// splitTop splits on sep outside parentheses and quotes.
func splitTop(s string, sep rune) []string {
	var out []string
	depth, quote := 0, rune(0)
	start := 0
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"' || r == '`':
			quote = r
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

type sqlTables struct {
	order []string
	byKey map[string]*Table
	cols  map[string][]string // from CREATE TABLE
}

func (st *sqlTables) get(name string, cols []string) *Table {
	key := strings.ToLower(name)
	t, ok := st.byKey[key]
	if !ok {
		if len(cols) == 0 {
			cols = st.cols[key]
		}
		if len(cols) > MaxCols {
			cols = cols[:MaxCols]
		}
		t = &Table{Name: name, Columns: cols, Rows: []map[string]string{}}
		st.byKey[key] = t
		st.order = append(st.order, key)
	}
	return t
}

func (st *sqlTables) add(t *Table, values []string) {
	t.Total++
	if len(t.Rows) >= MaxRows {
		t.Truncated = true
		return
	}
	for len(t.Columns) < len(values) && len(t.Columns) < MaxCols {
		t.Columns = append(t.Columns, "columna_"+itoa(len(t.Columns)+1))
	}
	rec := map[string]string{}
	for i, c := range t.Columns {
		if i < len(values) {
			rec[c] = values[i]
		}
	}
	t.Rows = append(t.Rows, rec)
}

func itoa(n int) string {
	b := []byte{}
	for n > 0 || len(b) == 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func readSQL(data []byte) ([]Table, error) {
	st := &sqlTables{byKey: map[string]*Table{}, cols: map[string][]string{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var stmt strings.Builder
	var copying *Table
	inQuote := false
	for sc.Scan() {
		line := sc.Text()
		if copying != nil {
			if line == `\.` {
				copying = nil
				continue
			}
			fields := strings.Split(line, "\t")
			for i, f := range fields {
				fields[i] = unescapeCopy(f)
			}
			st.add(copying, fields)
			continue
		}
		trim := strings.TrimSpace(line)
		if !inQuote && stmt.Len() == 0 && (trim == "" || strings.HasPrefix(trim, "--") || strings.HasPrefix(trim, "/*") || strings.HasPrefix(trim, "#")) {
			continue
		}
		if m := copyRe.FindStringSubmatch(line); m != nil && stmt.Len() == 0 {
			copying = st.get(ident(m[1]), identList(m[2]))
			continue
		}
		stmt.WriteString(line)
		stmt.WriteByte('\n')
		inQuote = endsInQuote(line, inQuote)
		if !inQuote && strings.HasSuffix(trim, ";") {
			st.statement(stmt.String())
			stmt.Reset()
		}
	}
	if stmt.Len() > 0 {
		st.statement(stmt.String())
	}
	var out []Table
	for _, k := range st.order {
		out = append(out, *st.byKey[k])
	}
	return out, nil
}

// endsInQuote tracks whether a statement continues inside a string.
func endsInQuote(line string, in bool) bool {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			if in {
				i++
			}
		case '\'':
			in = !in
		}
	}
	return in
}

func (st *sqlTables) statement(s string) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";"))
	if m := createRe.FindStringSubmatch(s); m != nil {
		var cols []string
		for _, def := range splitTop(m[2], ',') {
			def = strings.TrimSpace(def)
			if def == "" || constraintRe.MatchString(def) {
				continue
			}
			cols = append(cols, ident(strings.Fields(def)[0]))
		}
		st.cols[strings.ToLower(ident(m[1]))] = cols
		return
	}
	m := insertRe.FindStringSubmatch(s)
	if m == nil {
		return
	}
	t := st.get(ident(m[1]), identList(m[3]))
	for _, tuple := range tuples(m[4]) {
		st.add(t, tuple)
	}
}

// tuples reads "(1,'a b',NULL),(2,'it”s',3.5)".
func tuples(s string) [][]string {
	var out [][]string
	var cur []string
	var cell strings.Builder
	depth := 0
	quoted, wasQuoted := false, false
	flush := func() {
		v := cell.String()
		if !wasQuoted {
			v = strings.TrimSpace(v)
			if strings.EqualFold(v, "null") {
				v = ""
			}
		}
		cur = append(cur, v)
		cell.Reset()
		wasQuoted = false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quoted {
			switch {
			case c == '\\' && i+1 < len(s):
				i++
				cell.WriteByte(unescapeByte(s[i]))
			case c == '\'' && i+1 < len(s) && s[i+1] == '\'':
				i++
				cell.WriteByte('\'')
			case c == '\'':
				quoted = false
			default:
				cell.WriteByte(c)
			}
			continue
		}
		switch {
		case c == '\'':
			quoted, wasQuoted = true, true
		case c == '(':
			depth++
			if depth == 1 {
				cur = nil
				cell.Reset()
				continue
			}
			cell.WriteByte(c)
		case c == ')':
			depth--
			if depth == 0 {
				flush()
				out = append(out, cur)
				continue
			}
			cell.WriteByte(c)
		case c == ',' && depth == 1:
			flush()
		case depth >= 1:
			cell.WriteByte(c)
		}
	}
	return out
}

func unescapeByte(b byte) byte {
	switch b {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	case '0':
		return ' '
	}
	return b
}

// unescapeCopy reads one field of pg_dump's COPY text format.
func unescapeCopy(f string) string {
	if f == `\N` {
		return ""
	}
	if !strings.Contains(f, `\`) {
		return f
	}
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] == '\\' && i+1 < len(f) {
			i++
			b.WriteByte(unescapeByte(f[i]))
			continue
		}
		b.WriteByte(f[i])
	}
	return b.String()
}
