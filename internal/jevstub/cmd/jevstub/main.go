// Command jevstub serves recorded Jev answers, so the CLI can be exercised
// end to end without an API key.
package main

import (
	"fmt"
	"os"

	"github.com/SergeAx/scrutus/internal/jevstub"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: jevstub <fixtures.json>")
		os.Exit(2)
	}

	fixtures, err := jevstub.Load(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "jevstub: %v\n", err)
		os.Exit(2)
	}

	server := jevstub.New(fixtures)
	defer server.Close()
	fmt.Println(server.URL)

	select {}
}
