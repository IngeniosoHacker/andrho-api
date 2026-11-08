// Package intake reads the data a business brings from its previous system
// (any of CSV, Excel, JSON or a SQL dump, as it comes) and maps it to the
// structure of its AndRho ERP (Odoo).
//
// It is adapted from the ETL of OLAP-Data-lakehouse (etl-go/main.go):
// format detection by extension and then by content (GetFileFormat,
// identifyFormatByContent), one extractor per format (ExtractFromCSV,
// ExtractFromJSON, ExtractFromXLSX, ExtractFromXLS, ExtractFromDump) and the
// column analysis that told dimensions from facts by their names
// (AnalyzeColumnsWithLLM). Differences: it reads bytes (the upload), not
// files on disk; every sheet of a workbook and every table of a dump is
// read, not only the first; CSVs separated by ";" or tabs and saved in
// Latin-1 (Excel in Spanish) are read; dumps are actually parsed (INSERT
// and pg_dump's COPY); and instead of a star schema the analysis
// (mapping.go) picks the Odoo model each table belongs to.
package intake

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/extrame/xls"
	"github.com/tealeg/xlsx"
	"golang.org/x/text/encoding/charmap"
)

// Format of an uploaded file.
type Format string

const (
	CSV     Format = "csv"
	JSON    Format = "json"
	XLSX    Format = "xlsx"
	XLS     Format = "xls"
	SQL     Format = "sql"
	Unknown Format = ""
)

// Limits for one file.
const (
	MaxTables = 20
	MaxRows   = 5000 // per table, as internal/etl
	MaxCols   = 80
)

// ErrFormat is a file intake can't read.
var ErrFormat = errors.New("intake: formato no soportado")

// Table is one sheet, array or table of a file. Rows are keyed by Columns.
type Table struct {
	Name      string              `json:"name"`
	Columns   []string            `json:"columns"`
	Rows      []map[string]string `json:"-"`
	Total     int                 `json:"rows"`      // rows in the file (Rows stops at MaxRows)
	Truncated bool                `json:"truncated"` // more than MaxRows
}

// Detect tells the format from the name, then from the content
// (OLAP-Data-lakehouse: GetFileFormat + identifyFormatByContent).
func Detect(name string, data []byte) Format {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".csv", ".txt", ".tsv":
		return CSV
	case ".json":
		return JSON
	case ".xlsx", ".xlsm":
		return XLSX
	case ".xls":
		return XLS
	case ".sql", ".dump":
		return SQL
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	switch {
	case len(head) >= 4 && head[0] == 'P' && head[1] == 'K':
		return XLSX // a zip: Office Open XML
	case len(head) >= 4 && head[0] == 0xD0 && head[1] == 0xCF && head[2] == 0x11 && head[3] == 0xE0:
		return XLS // OLE2
	}
	t := strings.TrimSpace(strings.ToLower(string(head)))
	switch {
	case strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{"):
		return JSON
	case strings.Contains(t, "insert into") || strings.Contains(t, "create table") || strings.Contains(t, "copy ") || strings.HasPrefix(t, "--"):
		return SQL
	case strings.ContainsAny(t, ",;\t") && utf8.Valid(head):
		return CSV
	}
	return Unknown
}

// Read extracts the tables of a file.
func Read(name string, data []byte) (Format, []Table, error) {
	f := Detect(name, data)
	var tables []Table
	var err error
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	switch f {
	case CSV:
		var t Table
		t, err = readCSV(base, data)
		tables = []Table{t}
	case JSON:
		tables, err = readJSON(base, data)
	case XLSX:
		tables, err = readXLSX(data)
	case XLS:
		tables, err = readXLS(data)
	case SQL:
		tables, err = readSQL(data)
	default:
		return f, nil, ErrFormat
	}
	if err != nil {
		return f, nil, err
	}
	out := []Table{}
	for _, t := range tables {
		if len(t.Columns) > 0 && t.Total > 0 {
			out = append(out, t)
		}
		if len(out) == MaxTables {
			break
		}
	}
	if len(out) == 0 {
		return f, nil, fmt.Errorf("intake: el archivo no tiene filas con datos")
	}
	return f, out, nil
}

// text is the file as UTF-8: Excel in Spanish saves CSVs in Windows-1252.
func text(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if utf8.Valid(data) {
		return string(data)
	}
	s, err := charmap.Windows1252.NewDecoder().Bytes(data)
	if err != nil {
		return string(data)
	}
	return string(s)
}

// sniff picks the separator that splits the first lines most consistently.
func sniff(s string) rune {
	lines := strings.SplitN(s, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
	}
	best, bestScore := ',', -1
	for _, sep := range []rune{',', ';', '\t', '|'} {
		first := strings.Count(lines[0], string(sep))
		if first == 0 {
			continue
		}
		score := first * 10
		for _, l := range lines[1:] {
			if strings.TrimSpace(l) != "" && strings.Count(l, string(sep)) != first {
				score -= 5 // quoted separators make this approximate
			}
		}
		if score > bestScore {
			best, bestScore = sep, score
		}
	}
	return best
}

func readCSV(name string, data []byte) (Table, error) {
	s := text(data)
	r := csv.NewReader(strings.NewReader(s))
	r.Comma = sniff(s)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true
	var records [][]string
	for {
		rec, err := r.Read()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			if len(records) == 0 {
				return Table{}, fmt.Errorf("intake: CSV ilegible: %w", err)
			}
			break
		}
		records = append(records, rec)
		if len(records) > MaxRows+50 {
			break
		}
	}
	return fromGrid(name, records, len(records)), nil
}

// fromGrid builds a table from rows of cells: the header is the first row
// with at least two non-empty cells (sheets often start with a title).
func fromGrid(name string, grid [][]string, total int) Table {
	h, single := -1, -1
	for i, row := range grid {
		if i > 10 {
			break
		}
		n := 0
		for _, c := range row {
			if strings.TrimSpace(c) != "" {
				n++
			}
		}
		if n >= 2 {
			h = i
			break
		}
		if n == 1 && single < 0 {
			single = i // a one-column list, unless a wider header follows
		}
	}
	if h < 0 {
		h = single
	}
	t := Table{Name: name, Rows: []map[string]string{}}
	if h < 0 {
		return t
	}
	t.Columns = header(grid[h])
	for _, row := range grid[h+1:] {
		rec := map[string]string{}
		empty := true
		for j, col := range t.Columns {
			if j < len(row) {
				v := strings.TrimSpace(row[j])
				if v != "" {
					empty = false
				}
				rec[col] = v
			}
		}
		if empty {
			continue
		}
		t.Total++
		if len(t.Rows) < MaxRows {
			t.Rows = append(t.Rows, rec)
		}
	}
	if total > len(grid) {
		t.Total += total - len(grid)
	}
	t.Truncated = t.Total > len(t.Rows)
	return t
}

// header cleans column names: blanks get a name, repeats a suffix.
func header(row []string) []string {
	seen := map[string]int{}
	var cols []string
	for i, c := range row {
		if i == MaxCols {
			break
		}
		c = strings.Join(strings.Fields(c), " ")
		if c == "" {
			c = "columna_" + strconv.Itoa(i+1)
		}
		if n := seen[strings.ToLower(c)]; n > 0 {
			seen[strings.ToLower(c)]++
			c = c + "_" + strconv.Itoa(n+1)
		} else {
			seen[strings.ToLower(c)] = 1
		}
		cols = append(cols, c)
	}
	return cols
}

// maxUnzipped caps what a workbook may expand to (a zip bomb check).
const maxUnzipped = 200 << 20

func readXLSX(data []byte) ([]Table, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("intake: Excel ilegible: %w", err)
	}
	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
	}
	if total > maxUnzipped {
		return nil, fmt.Errorf("intake: el Excel es demasiado grande por dentro")
	}
	f, err := xlsx.OpenBinary(data)
	if err != nil {
		return nil, fmt.Errorf("intake: Excel ilegible: %w", err)
	}
	var out []Table
	for _, sh := range f.Sheets {
		var grid [][]string
		for i, row := range sh.Rows {
			if i > MaxRows+50 {
				break
			}
			var cells []string
			if row != nil {
				for _, c := range row.Cells {
					cells = append(cells, cellText(c))
				}
			}
			grid = append(grid, cells)
		}
		out = append(out, fromGrid(sh.Name, grid, len(sh.Rows)))
	}
	return out, nil
}

// cellText is what the cell shows; dates stay readable (not serials).
func cellText(c *xlsx.Cell) string {
	if c == nil {
		return ""
	}
	if c.IsTime() {
		if t, err := c.GetTime(false); err == nil {
			if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
				return t.Format("2006-01-02")
			}
			return t.Format("2006-01-02 15:04")
		}
	}
	if s, err := c.FormattedValue(); err == nil {
		return s
	}
	return c.String()
}

func readXLS(data []byte) ([]Table, error) {
	wb, err := xls.OpenReader(bytes.NewReader(data), "utf-8")
	if err != nil {
		return nil, fmt.Errorf("intake: Excel 97 ilegible: %w", err)
	}
	var out []Table
	for i := 0; i < wb.NumSheets(); i++ {
		sh := wb.GetSheet(i)
		if sh == nil {
			continue
		}
		var grid [][]string
		for r := 0; r <= int(sh.MaxRow) && r <= MaxRows+50; r++ {
			row := sh.Row(r)
			var cells []string
			if row != nil {
				for c := row.FirstCol(); c < row.LastCol(); c++ {
					for len(cells) < c {
						cells = append(cells, "")
					}
					cells = append(cells, row.Col(c))
				}
			}
			grid = append(grid, cells)
		}
		out = append(out, fromGrid(sh.Name, grid, int(sh.MaxRow)+1))
	}
	return out, nil
}

// readJSON takes an array of objects, an object of arrays (one table per
// key) or a single object; nested objects are flattened ("cliente.nombre").
func readJSON(name string, data []byte) ([]Table, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("intake: JSON ilegible: %w", err)
	}
	switch x := v.(type) {
	case []any:
		return []Table{jsonTable(name, x)}, nil
	case map[string]any:
		var out []Table
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if arr, ok := x[k].([]any); ok && len(arr) > 0 {
				if _, isObj := arr[0].(map[string]any); isObj {
					out = append(out, jsonTable(k, arr))
				}
			}
		}
		if len(out) == 0 {
			out = []Table{jsonTable(name, []any{x})}
		}
		return out, nil
	}
	return nil, fmt.Errorf("intake: el JSON no tiene registros")
}

func jsonTable(name string, arr []any) Table {
	t := Table{Name: name, Rows: []map[string]string{}}
	index := map[string]bool{}
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rec := map[string]string{}
		flatten("", obj, rec)
		t.Total++
		if len(t.Rows) >= MaxRows {
			continue
		}
		for k := range rec {
			if !index[k] && len(t.Columns) < MaxCols {
				index[k] = true
				t.Columns = append(t.Columns, k)
			}
		}
		t.Rows = append(t.Rows, rec)
	}
	t.Truncated = t.Total > len(t.Rows)
	return t
}

func flatten(prefix string, obj map[string]any, out map[string]string) {
	for k, v := range obj {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch x := v.(type) {
		case map[string]any:
			flatten(key, x, out)
		case []any:
			parts := []string{}
			for _, e := range x {
				if _, nested := e.(map[string]any); !nested {
					parts = append(parts, fmt.Sprint(e))
				}
			}
			out[key] = strings.Join(parts, ", ")
		case nil:
			out[key] = ""
		default:
			out[key] = fmt.Sprint(x)
		}
	}
}
