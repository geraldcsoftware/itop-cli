package itop

import "strings"

// Quote returns v as a single-quoted OQL string literal, escaping backslash
// first and then single quote, each with a backslash. Every user-supplied
// value placed in an OQL query goes through here.
func Quote(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}
