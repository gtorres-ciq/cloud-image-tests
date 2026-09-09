// cmd/citrun/main.go
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "resume":
		os.Exit(cmdResume(os.Args[2:], false))
	case "rerun-failed":
		os.Exit(cmdResume(os.Args[2:], true))
	case "report":
		os.Exit(cmdReport(os.Args[2:]))
	case "status":
		os.Exit(cmdStatus(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: citrun <run|resume|rerun-failed|status|report> [flags]`)
}
