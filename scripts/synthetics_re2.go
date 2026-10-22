//go:build ignore

// RE2 checks for scripts/synthetics.sh. Synthetic Monitoring runs Go's regexp (RE2), which
// is not jq's Oniguruma: lookarounds and backreferences do not compile there.
//
//	go run scripts/synthetics_re2.go compile            regexps on stdin, one per line: each must compile
//	go run scripts/synthetics_re2.go match <file>       regexps on stdin: each must match the file's content
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
)

func main() {
	ok := len(os.Args) == 2 && os.Args[1] == "compile" || len(os.Args) == 3 && os.Args[1] == "match"
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: synthetics_re2.go compile | match <file>")
		os.Exit(2)
	}
	var content []byte
	if os.Args[1] == "match" {
		b, err := os.ReadFile(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		content = b
	}
	bad := 0
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		re, err := regexp.Compile(sc.Text())
		if err != nil {
			fmt.Printf("does not compile as RE2: %s: %v\n", sc.Text(), err)
			bad++
			continue
		}
		if content != nil && !re.Match(content) {
			fmt.Printf("does not match %s: %s\n", os.Args[2], sc.Text())
			bad++
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if bad > 0 {
		os.Exit(1)
	}
}
