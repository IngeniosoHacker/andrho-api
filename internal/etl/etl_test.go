package etl

import "testing"

func TestParseAmount(t *testing.T) {
	for in, want := range map[string]float64{
		"Q1,250.50": 1250.5, "1.250,50": 1250.5, "$ 12": 12, "12": 12, "1,250": 1250, "1.250": 1250, "3,5": 3.5, "Q 0.99": 0.99,
	} {
		if got, ok := ParseAmount(in); !ok || got != want {
			t.Errorf("ParseAmount(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	if _, ok := ParseAmount("abc"); ok {
		t.Error("abc parsed")
	}
}

func TestTransform(t *testing.T) {
	recs, issues := Transform("products", []map[string]string{
		{"name": " Pan  francés ", "code": "PF1", "price": "Q1.50", "qty": "80"},
		{"name": "", "price": "3"},
		{"name": "Champurrada", "price": "tres"},
		{"name": "Pan francés v2", "code": "pf1", "price": "2"},
		{"name": "Café", "cost": "2,50"},
	})
	if len(recs) != 2 {
		t.Fatalf("records = %+v", recs)
	}
	if recs[0].Values["name"] != "Pan francés v2" || recs[0].Nums["price"] != 2 {
		t.Errorf("duplicate code should keep the last row: %+v", recs[0])
	}
	if recs[1].Nums["cost"] != 2.5 {
		t.Errorf("cost = %v", recs[1].Nums["cost"])
	}
	if len(issues) != 3 {
		t.Errorf("issues = %+v", issues)
	}

	recs, issues = Transform("customers", []map[string]string{
		{"name": "Ana", "email": "ANA@x.gt"}, {"name": "Ana López", "email": "ana@x.gt"}, {"name": "Beto", "email": "beto@"},
	})
	if len(recs) != 1 || recs[0].Values["email"] != "ana@x.gt" || len(issues) != 2 {
		t.Errorf("customers = %+v / %+v", recs, issues)
	}
}
