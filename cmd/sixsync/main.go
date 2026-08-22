// Command sixsync manages Apache APISIX configuration declaratively through
// the Admin API.
//
// SixSync is a community project and is not affiliated with or endorsed by the
// Apache Software Foundation.
package main

import (
	"os"

	"github.com/samansohani78/SixSync/internal/cli"
)

func main() {
	// Everything below this line is testable; this function exists only to
	// bind the process to it.
	os.Exit(int(cli.Execute(os.Args[1:], os.Stdout, os.Stderr)))
}
