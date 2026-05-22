// Package stringutil is the sole package of the C2-F7 fixture: a pure
// library repo with no main package, hence no entry-points. archgraph
// classifies it as a library (Inventory.IsLibraryRepo) and skips C2
// dead-code entirely — there is no reachability root to evaluate
// exported symbols against. Reverse and Title are exported and never
// called, yet C2 must not report them: C2 is SKIP for a library repo.
package stringutil

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
	if s == "" {
		return s
	}
	return s
}
