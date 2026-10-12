package handlers

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"¿Qué software necesita mi emprendimiento?": "que-software-necesita-mi-emprendimiento",
		"  Big Data & Data Science: casos de éxito ": "big-data-data-science-casos-de-exito",
		"Año 2026 — niño":                            "ano-2026-nino",
		"---":                                        "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	long := Slugify("palabra palabra palabra palabra palabra palabra palabra palabra palabra palabra palabra palabra palabra")
	if len(long) > maxArticleSlug || long[len(long)-1] == '-' {
		t.Errorf("long slug not cut on a word boundary: %q", long)
	}
}

func TestArticleRequestClean(t *testing.T) {
	req := articleRequest{Title: "Hola mundo", Status: "PUBLICADO", Body: "texto", Keywords: []string{" ERP ", "erp", ""}}
	if msg := req.clean(); msg != "" {
		t.Fatalf("unexpected error %q", msg)
	}
	if req.Slug != "hola-mundo" || req.Status != "publicado" || len(req.Keywords) != 1 || req.Keywords[0] != "erp" {
		t.Errorf("clean() = %+v", req)
	}
	bad := []articleRequest{
		{Title: ""},
		{Title: "x", Status: "otro"},
		{Title: "x", Status: "publicado"},
		{Title: "x", Path: "articulos/x"},
		{Title: "x", Path: "/a b"},
	}
	for _, r := range bad {
		if msg := r.clean(); msg == "" {
			t.Errorf("clean(%+v) accepted an invalid article", r)
		}
	}
}
