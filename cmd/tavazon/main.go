// Command tavazon manufactures balancing UDP traffic so an asymmetric hosting
// quota stays healthy. See docs/project.md for the full design.
package main

import (
	"flag"
	"fmt"

	_ "time/tzdata" // embed zoneinfo (~450 KB) so Asia/Tehran resolves on scratch/distroless
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("tavazon", version)
		return
	}
	fmt.Println("tavazon: not yet implemented")
}
