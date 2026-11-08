package intake

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tealeg/xlsx"
	"golang.org/x/text/encoding/charmap"
)

func TestDetect(t *testing.T) {
	for name, c := range map[string]struct {
		file string
		data string
		want Format
	}{
		"csv ext":     {"a.csv", "x", CSV},
		"json sniff":  {"datos", `[{"a":1}]`, JSON},
		"sql sniff":   {"respaldo", "INSERT INTO t VALUES (1);", SQL},
		"xlsx sniff":  {"archivo", "PK\x03\x04....", XLSX},
		"csv sniff":   {"lista", "a;b\n1;2", CSV},
		"binary junk": {"x.bin", "\x00\x01\x02\x03", Unknown},
	} {
		if got := Detect(c.file, []byte(c.data)); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestReadCSVSemicolonLatin1(t *testing.T) {
	src := "Listado de productos;;\nCódigo;Descripción;Precio de venta (Q);Existencia\nPF1;Pan francés;1,50;80\n;;;\nCH2;\"Champurrada; grande\";3,00;12\n"
	latin, _ := charmap.Windows1252.NewEncoder().Bytes([]byte(src))
	f, tables, err := Read("productos.csv", latin)
	if err != nil || f != CSV || len(tables) != 1 {
		t.Fatalf("read: %v %v %d", err, f, len(tables))
	}
	tb := tables[0]
	if strings.Join(tb.Columns, "|") != "Código|Descripción|Precio de venta (Q)|Existencia" {
		t.Fatalf("columns = %q", tb.Columns)
	}
	if tb.Total != 2 || tb.Rows[1]["Descripción"] != "Champurrada; grande" {
		t.Fatalf("rows = %+v", tb.Rows)
	}
	m := Analyze(tb, "productos.csv")
	if m.Kind != "products" || m.Confidence != High || !m.Load {
		t.Fatalf("mapping = %+v", m)
	}
	want := map[string]string{"Código": "code", "Descripción": "name", "Precio de venta (Q)": "price", "Existencia": "qty"}
	for c, f := range want {
		if m.Columns[c] != f {
			t.Errorf("%s → %q, want %q", c, m.Columns[c], f)
		}
	}
	rows := m.Apply(tb)
	if rows[0]["price"] != "1,50" || rows[0]["name"] != "Pan francés" {
		t.Errorf("apply = %+v", rows[0])
	}
}

func TestReadJSONObjectOfArrays(t *testing.T) {
	data := `{"clientes":[{"nombre":"Ana","contacto":{"correo":"ana@x.gt","tel":"+502 5555 5555"}},{"nombre":"Beto","contacto":{"correo":"b@x.gt"}}],
	          "proveedores":[{"razon_social":"Harinas SA","nit":"123-4","telefono":"2222 3333"}], "meta":{"v":1}}`
	_, tables, err := Read("export.json", []byte(data))
	if err != nil || len(tables) != 2 {
		t.Fatalf("tables = %+v, %v", tables, err)
	}
	c := Analyze(tables[0], "export.json")
	if tables[0].Name != "clientes" || c.Kind != "customers" || c.Columns["contacto.correo"] != "email" || c.Columns["contacto.tel"] != "phone" {
		t.Errorf("clientes = %+v", c)
	}
	p := Analyze(tables[1], "export.json")
	if p.Kind != "suppliers" || p.Columns["razon_social"] != "name" || p.Columns["nit"] != "vat" {
		t.Errorf("proveedores = %+v", p)
	}
}

func TestReadSQLDump(t *testing.T) {
	dump := `-- MySQL dump
CREATE TABLE ` + "`productos`" + ` (
  ` + "`id`" + ` int NOT NULL,
  ` + "`nombre`" + ` varchar(100),
  ` + "`precio`" + ` decimal(10,2),
  PRIMARY KEY (` + "`id`" + `)
);
INSERT INTO ` + "`productos`" + ` VALUES (1,'Pan, francés',1.50),(2,'It''s pan',NULL),
(3,'Multi
línea',2.00);
COPY public.clientes (id, nombre, correo) FROM stdin;
1	Ana	ana@x.gt
2	Beto\tB	\N
\.
`
	f, tables, err := Read("respaldo.sql", []byte(dump))
	if err != nil || f != SQL || len(tables) != 2 {
		t.Fatalf("read: %v %v %+v", err, f, tables)
	}
	p := tables[0]
	if p.Name != "productos" || p.Total != 3 || p.Rows[0]["nombre"] != "Pan, francés" || p.Rows[1]["nombre"] != "It's pan" || p.Rows[1]["precio"] != "" || p.Rows[2]["nombre"] != "Multi\nlínea" {
		t.Fatalf("productos = %+v", p)
	}
	c := tables[1]
	if c.Name != "clientes" || c.Rows[1]["nombre"] != "Beto\tB" || c.Rows[1]["correo"] != "" {
		t.Fatalf("clientes = %+v", c)
	}
	if m := Analyze(c, "respaldo.sql"); m.Kind != "customers" {
		t.Errorf("clientes kind = %q", m.Kind)
	}
}

func TestReadXLSXAndSales(t *testing.T) {
	f := xlsx.NewFile()
	sh, _ := f.AddSheet("Ventas enero")
	for _, r := range [][]string{{"Fecha", "Factura", "Cliente", "Producto", "Cantidad", "Total"}, {"2026-01-03", "A-1", "Ana", "Pan", "3", "4.50"}, {"2026-01-04", "A-2", "Beto", "Café", "1", "12"}} {
		row := sh.AddRow()
		for _, v := range r {
			row.AddCell().SetString(v)
		}
	}
	sh2, _ := f.AddSheet("Personal")
	for _, r := range [][]string{{"Nombre completo", "Puesto", "Departamento", "Salario"}, {"Luis", "Panadero", "Producción", "4000"}} {
		row := sh2.AddRow()
		for _, v := range r {
			row.AddCell().SetString(v)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	format, tables, err := Read("negocio.xlsx", buf.Bytes())
	if err != nil || format != XLSX || len(tables) != 2 {
		t.Fatalf("read: %v %v %d", err, format, len(tables))
	}
	s := Analyze(tables[0], "negocio.xlsx")
	if s.Kind != "sales" || s.Load || s.Confidence != Mid || s.Columns["Total"] != "total" {
		t.Errorf("sales = %+v", s)
	}
	e := Analyze(tables[1], "negocio.xlsx")
	if e.Kind != "employees" || e.Load || e.Columns["Salario"] != "salary" {
		t.Errorf("employees = %+v", e)
	}
}

func TestUnknownTable(t *testing.T) {
	tb := Table{Name: "hoja1", Columns: []string{"x", "y", "monto"}, Total: 1, Rows: []map[string]string{{"x": "a", "y": "b", "monto": "10"}}}
	m := Analyze(tb, "algo.csv")
	if m.Kind != "" || m.Confidence != Low || m.Load {
		t.Fatalf("mapping = %+v", m)
	}
	if len(m.Facts) != 1 || m.Facts[0] != "monto" || len(m.Dimensions) != 2 {
		t.Errorf("facts/dims = %v / %v", m.Facts, m.Dimensions)
	}
}
