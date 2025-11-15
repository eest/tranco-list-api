package main

import (
	"log"

	"github.com/eest/tranco-list-api/pkg/tlapi"
)

func main() {
	err := tlapi.RunDBWriter()
	if err != nil {
		log.Fatal(err)
	}
}
