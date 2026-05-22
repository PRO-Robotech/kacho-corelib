// Package stringutil is the sole package of a library repo: no main
// package, hence no entry-points. archgraph classifies such a repo as a
// library and reports zero entry-points discovered.
package stringutil

import "strings"

// Reverse returns s with its runes in reverse order.
func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// Title upper-cases the first rune of every space-separated word.
func Title(s string) string {
	return strings.Title(s) //nolint:staticcheck // fixture only
}
