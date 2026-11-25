package nutrition

import "strings"

// The normalized pairs cover common generic foods. They supply a fallback query
// only; catalog labels, source claims and food IDs still come from the database.
var commonGermanEnglish = [][2]string{
	{"apfel", "apple"}, {"apfelsaft", "apple juice"}, {"banane", "banana"}, {"birne", "pear"},
	{"erdbeere", "strawberry"}, {"heidelbeere", "blueberry"},
	{"weintrauben", "grapes"}, {"karotte", "carrot"}, {"gurke", "cucumber"},
	{"tomate", "tomato"}, {"paprika", "sweet pepper"}, {"kartoffel", "potato"},
	{"susskartoffel", "sweet potato"}, {"reis", "rice"}, {"naturreis", "brown rice"},
	{"haferflocken", "oats"}, {"vollkornbrot", "whole wheat bread"},
	{"hahnchenbrust", "chicken breast"}, {"lachs", "salmon"}, {"thunfisch", "tuna"},
	{"linsen", "lentils"}, {"kichererbsen", "chickpeas"}, {"mandeln", "almonds"},
	{"walnusse", "walnuts"}, {"joghurt", "yogurt"}, {"milch", "milk"},
	{"ei", "egg"}, {"olivenol", "olive oil"}, {"rapsol", "canola oil"},
}

// CatalogSearchAlias returns one secondary term for an exact DE or EN food word.
// Short prefixes retain the existing indexed search for type-ahead.
func CatalogSearchAlias(query, language, market string) string {
	base, _, _ := strings.Cut(language, "-")
	if base != "de" && base != "en" && market != "DE" && market != "AT" && market != "CH" {
		return ""
	}
	for _, pair := range commonGermanEnglish {
		switch query {
		case pair[0]:
			return pair[1]
		case pair[1]:
			return pair[0]
		}
	}
	return ""
}
