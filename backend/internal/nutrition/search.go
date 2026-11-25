package nutrition

import "strings"

const minimumCatalogAliasPrefix = 4 // D283

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

// CatalogSearchAlias returns one secondary term for an exact DE or EN food name
// or a prefix whose alternatives can be found with a single catalog term.
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
	if len(query) < minimumCatalogAliasPrefix {
		return ""
	}
	var alternatives []string
	var shortest string
	for _, pair := range commonGermanEnglish {
		for i, term := range pair {
			if !strings.HasPrefix(term, query) {
				continue
			}
			alternative := pair[1-i]
			alternatives = append(alternatives, alternative)
			if shortest == "" || len(alternative) < len(shortest) {
				shortest = alternative
			}
		}
	}
	// "apfe" covers both "apple" and "apple juice". Unrelated alternates
	// such as those for "sweet" stay on the primary search alone.
	for _, alternative := range alternatives {
		if !strings.HasPrefix(alternative, shortest) {
			return ""
		}
	}
	return shortest
}
