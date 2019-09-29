package main

import (
	_ "expvar"
	"github.com/eest/tranco-list-api/pkg/tlapi"
	_ "github.com/lib/pq"
	"log"
)

func main() {
	err := tlapi.RunAPIService()
	if err != nil {
		log.Fatal(err)
	}
}
