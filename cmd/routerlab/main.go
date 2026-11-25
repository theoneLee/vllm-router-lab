// Command routerlab runs CPU-hosted vLLM workers and a replay client for
// testing the vLLM Router.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/theoneLee/vllm-router-lab/internal/fleet"
	"github.com/theoneLee/vllm-router-lab/internal/replay"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: routerlab fleet|replay [flags]")
	}
	var e error
	switch os.Args[1] {
	case "fleet":
		e = fleet.Run(os.Args[2:])
	case "replay":
		e = replay.Run(os.Args[2:])
	default:
		e = fmt.Errorf("unknown command %s", os.Args[1])
	}
	if e != nil {
		log.Fatal(e)
	}
}
