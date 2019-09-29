package main

import (
	"github.com/eest/tranco-list-api/pkg/tlapi"
	"log"
)

func main() {
	err := tlapi.RunDBWriter()
	if err != nil {
		log.Fatal(err)
	}
}
