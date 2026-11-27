package nutrition

import "testing"

func TestCatalogSearchAlias(t *testing.T) {
	for _, tc := range []struct{ query, language, market, want string }{
		{"Apfe", "de-DE", "", "apple"},
		{"Apfels", "de", "", "apple juice"},
		{"Apfel", "de", "", "apple"},
		{"Apfelsaft", "de", "", "apple juice"},
		{"Appl", "en-US", "", "apfel"},
		{"Apple Juice", "en", "", "apfelsaft"},
		{"Hähn", "fr", "CH", "chicken breast"},
		{"Apfe", "fr", "FR", ""},
		{"Apf", "de", "DE", ""},
		{"Sweet", "en", "", ""},
		{"Quinoa", "en", "", ""},
		{"Ei", "de", "", "egg"},
	} {
		t.Run(tc.query+"/"+tc.language, func(t *testing.T) {
			if got := CatalogSearchAlias(Normalize(tc.query), tc.language, tc.market); got != tc.want {
				t.Errorf("alias = %q, want %q", got, tc.want)
			}
		})
	}
}
