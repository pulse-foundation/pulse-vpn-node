// Test-only fixture binary. It is never included in the agent runtime image.
package main

import (
	"fmt"
	"os"
)

const fixtureDir = "/fixtures"

func main() {
	var err error
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "expected fixture command")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "generate":
		err = generate()
	case "target":
		err = serveTarget()
	case "target-health":
		err = targetHealth()
	case "probe":
		err = probe()
	case "reject-bad-uuid":
		err = expectRejected("bad-uuid:7890")
	case "reject-bad-key":
		err = expectRejected("bad-key:7890")
	case "reject-stopped-node":
		err = expectRejected("client:7890")
	default:
		err = fmt.Errorf("unknown fixture command")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
