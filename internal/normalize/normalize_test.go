package normalize

import "testing"

func TestExtractSKU(t *testing.T) {
	cases := []struct {
		store, url, wantSKU string
		wantOK              bool
	}{
		{"Auchan", "https://www.auchan.pt/pt/beleza-e-higiene/champo-e-coloracao/champo-tratamento/champo-syoss-intense-plex-440-ml/3743248.html", "3743248", true},
		{"Continente", "https://www.continente.pt/produto/coloracao-permanente-ultra-color-vermelho-ousado-5.62-garnier-nutrisse-4717428.html", "4717428", true},
		{"Pingo Doce", "https://www.pingodoce.pt/home/produtos/mercearia/temperos-e-molhos/maionese-mostarda-e-ketchup/ketchup-top-down-calve-728590.html", "728590", true},
		{"Celeiro", "https://www.celeiro.pt/566834-nutrikings-calm-150-ml-ltr-dietmed", "566834", true},
		// Non-product URL that leaked into the Continente sitemap scrape.
		{"Continente", "https://www.continente.pt/produto/sabores-tradicionais-foodrefeicoestradicionais.html", "", false},
	}

	for _, c := range cases {
		sku, ok := ExtractSKU(c.store, c.url)
		if ok != c.wantOK || sku != c.wantSKU {
			t.Errorf("ExtractSKU(%q, %q) = (%q, %v), want (%q, %v)", c.store, c.url, sku, ok, c.wantSKU, c.wantOK)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"CHAMPÔ SYOSS INTENSE PLEX 440 ML":              "champo syoss intense plex 440 ml",
		"Queijo Florette de Cabra Pavé D'Affinois":      "queijo florette cabra pave d affinois",
		"Champô e Condicionador Barba Original Bulldog": "champo e condicionador barba original bulldog",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePackSize(t *testing.T) {
	cases := []struct {
		store, name, description string
		wantValue                float64
		wantUnit                 Unit
		wantCount                int
		wantOK                   bool
	}{
		{
			store: "Pingo Doce", name: "Ketchup Redução de Açúcar e Sal Top Down",
			description: "0.555 Kg | 8,99 €/Kg | Categoria: Maionese, Mostarda e Ketchup",
			wantValue:   555, wantUnit: UnitGram, wantCount: 1, wantOK: true,
		},
		{
			// Fresh produce: qty+unit with no unit-price segment at all.
			store: "Pingo Doce", description: "2.05 Kg | Categoria: Frutas",
			wantValue: 2050, wantUnit: UnitGram, wantCount: 1, wantOK: true,
		},
		{
			// Multipack.
			store: "Pingo Doce", description: "2 x1.75 L | 1,05 €/L | Categoria: Refrigerante com Gás",
			wantValue: 3500, wantUnit: UnitMl, wantCount: 2, wantOK: true,
		},
		{
			store: "Celeiro", description: "150 ML • 97.66€ por Ltr. | Alimentação Saudável",
			wantValue: 150, wantUnit: UnitMl, wantCount: 1, wantOK: true,
		},
		{
			store: "Celeiro", description: "60 Comprimidos • 0.27€ por Comp. | Alimentação Saudável",
			wantValue: 60, wantUnit: UnitUn, wantCount: 60, wantOK: true,
		},
		{
			store: "Auchan", name: "CHAMPÔ SYOSS INTENSE PLEX 440 ML",
			description: "9.07 €/Lt | SYOSS SHP INT PLEX",
			wantValue:   440, wantUnit: UnitMl, wantCount: 1, wantOK: true,
		},
		{
			store: "Auchan", name: "MÁSCARA BIOTEN HIDROGEL HYDRO XCELL 1 UN",
			description: "2.99 €/un | HIDROGEL HYDRO XCELL",
			wantValue:   1, wantUnit: UnitUn, wantCount: 1, wantOK: true,
		},
		{
			store: "Continente", description: "emb. 200 ml | Desodorizante All Over Body Rexona Men Ocean Rush",
			wantValue: 200, wantUnit: UnitMl, wantCount: 1, wantOK: true,
		},
		{
			store: "Continente", description: "garrafa 75 cl",
			wantValue: 750, wantUnit: UnitMl, wantCount: 1, wantOK: true,
		},
		{
			// Fallback: quantity not at the very start of the description.
			store: "Continente", description: "O Leite de Pastagem meio-gordo emb. 1 lt é um leite de elevada qualidade",
			wantValue: 1000, wantUnit: UnitMl, wantCount: 1, wantOK: true,
		},
		{
			// Genuinely no pack info (book-price-regulation blurb) — must
			// fail cleanly, not false-positive on an unrelated number.
			store: "Continente", description: "Onde se lê promoção na frase abaixo, deverá ler-se fixação de preço",
			wantOK: false,
		},
	}

	for _, c := range cases {
		got, ok := ParsePackSize(c.store, c.name, c.description)
		if ok != c.wantOK {
			t.Errorf("ParsePackSize(%q, %q, %q) ok = %v, want %v (got %+v)", c.store, c.name, c.description, ok, c.wantOK, got)
			continue
		}
		if !ok {
			continue
		}
		if got.TotalValue != c.wantValue || got.Unit != c.wantUnit || got.UnitCount != c.wantCount {
			t.Errorf("ParsePackSize(%q, %q, %q) = %+v, want {%v %v %v}",
				c.store, c.name, c.description, got, c.wantValue, c.wantUnit, c.wantCount)
		}
	}
}
