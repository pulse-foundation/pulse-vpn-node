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
	case "traffic-live":
		err = liveTraffic()
	case "traffic-persist":
		err = persistedTraffic()
	case "users-live":
		err = liveUsers()
	case "users-persist":
		err = persistedUsers()
	case "telemetry-live":
		err = liveTelemetry()
	case "telemetry-persist":
		err = persistedTelemetry()
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
		if os.Args[1] == "telemetry-live" || os.Args[1] == "telemetry-persist" || os.Args[1] == "users-live" || os.Args[1] == "users-persist" || os.Args[1] == "traffic-live" || os.Args[1] == "traffic-persist" {
			fmt.Println("FAIL: " + err.Error())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
