package recommend

import "testing"

func TestRecommendProfiles(t *testing.T) {
	cases := []struct {
		name string
		a    Answers
		want string
	}{
		{
			name: "small shop happy with its software, wants one area",
			a: Answers{
				Sectors: []string{"comercio"}, CompanySize: "1-10", SalesMethods: []string{"tienda"},
				Management: "software", Satisfaction: 5, Modules: []string{"inventario"},
				Expectations: []string{"organizarse"}, HasWebsite: "si", SocialNetworks: []string{"facebook"},
			},
			want: PlanBasico,
		},
		{
			name: "online seller without web chasing customers",
			a: Answers{
				Sectors: []string{"comercio"}, CompanySize: "1-10", SalesMethods: []string{"redes", "en_linea", "domicilio"},
				Management: "software", Satisfaction: 4, Modules: []string{"marketing", "ventas", "clientes"},
				Expectations: []string{"mas_clientes", "vender_mas", "mas_seguidores"}, HasWebsite: "no",
				SocialNetworks: []string{"instagram"}, MessagingApps: []string{"whatsapp_business"},
			},
			want: PlanCrecer,
		},
		{
			name: "restaurant on paper losing stock, wants many areas",
			a: Answers{
				Sectors: []string{"restaurantes", "comercio"}, CompanySize: "11-50", SalesMethods: []string{"tienda"},
				RestaurantExp: "no_controlo", UnsoldHandling: "se_pierde", Management: "papel",
				PainPoints:   []string{"datos_dispersos", "pierdo_control"},
				Modules:      []string{"finanzas", "inventario", "compras", "ventas", "rrhh"},
				Expectations: []string{"organizarse", "reducir_perdidas", "automatizar"}, HasWebsite: "si",
				SocialNetworks: []string{"facebook"},
			},
			want: PlanTodo,
		},
	}
	for _, c := range cases {
		got := Recommend(c.a)
		if got.Plan != c.want {
			t.Errorf("%s: got %s (scores %v), want %s", c.name, got.Plan, got.Scores, c.want)
		}
		if len(got.Reasons) == 0 {
			t.Errorf("%s: no reasons", c.name)
		}
	}
}

func TestEmptyAnswersStillRecommend(t *testing.T) {
	got := Recommend(Answers{})
	if got.Plan == "" || len(got.Modules) == 0 || got.Modules[0] != "analitica" {
		t.Fatalf("unexpected result for empty answers: %+v", got)
	}
}

func TestSuggestModulesDropsUnknown(t *testing.T) {
	got := SuggestModules(Answers{Modules: []string{"inventario", "<script>"}, HasWebsite: "si"}, PlanBasico)
	for _, m := range got {
		if m == "<script>" {
			t.Fatal("unknown module kept")
		}
	}
}
