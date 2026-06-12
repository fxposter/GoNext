package main

import (
	"fmt"
	"os"

	"gonext/internal/transpiler"
)

func main() {
	if len(os.Args) < 3 {
		die("usage: gonext <transpile|run> <file.gn>")
	}

	switch os.Args[1] {
	case "transpile":
		out, err := transpiler.TranspileFile(os.Args[2])
		if err != nil {
			die(err.Error())
		}
		fmt.Print(out)
	case "run":
		if err := transpiler.RunFile(os.Args[2]); err != nil {
			die(err.Error())
		}
	default:
		die("unknown command: " + os.Args[1])
	}
}

func die(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
