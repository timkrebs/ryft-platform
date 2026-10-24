package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// splitPath splits a mutation path; "~" stands for a "/" inside a key
// (annotation keys such as kubectl.kubernetes.io~restartedAt).
func splitPath(p string) []string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = strings.ReplaceAll(parts[i], "~", "/")
	}
	return parts
}

func jm(s string) map[string]any { return jv(s).(map[string]any) }

var fixtureObjects map[string]map[string]any

// fx returns a copy of a hand-written object from testdata/objects.json (the harness runs
// with the celtest directory as its working directory).
func fx(name string) map[string]any {
	if fixtureObjects == nil {
		b, err := os.ReadFile("testdata/objects.json")
		must(err)
		must(json.Unmarshal(b, &fixtureObjects))
	}
	o, ok := fixtureObjects[name]
	if !ok {
		panic(fmt.Sprintf("testdata/objects.json has no %q", name))
	}
	return deepCopy(o)
}
