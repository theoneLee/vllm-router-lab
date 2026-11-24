package main

import (
	"fmt"
	"log"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: routerlab fleet|replay [flags]")
	}
	var e error
	switch os.Args[1] {
	case "fleet":
		e = fleet(os.Args[2:])
	case "replay":
		e = replay(os.Args[2:])
	default:
		e = fmt.Errorf("unknown command %s", os.Args[1])
	}
	if e != nil {
		log.Fatal(e)
	}
}
