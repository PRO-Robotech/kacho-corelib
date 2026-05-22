// Command svc is an intentionally broken main package used as an
// archgraph fixture: it references an undefined identifier so the
// package fails to type-check.
package main

func main() {
	// thisIdentifierDoesNotExist is undefined on purpose.
	thisIdentifierDoesNotExist()
}
