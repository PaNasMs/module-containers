package main

import (
	"github.com/PaNasMs/module-containers/internal/engine"
	"github.com/PaNasMs/module-sdk/modulehost"
	"log"
	"net/http"
)

func main() {
	e, err := engine.Open("/var/lib/panasms-containers")
	if err != nil {
		log.Fatal(err)
	}
	modulehost.ServeWithPassivePaths("containers", e.Active, map[string]bool{"/events": true}, func(allowed map[string]bool) http.Handler { return e.Handler(allowed) })
}
