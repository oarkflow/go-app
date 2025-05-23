package main

import (
	"flag"
	"log"
	
	"github.com/oarkflow/dag/nlp/server"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	data := flag.String("data", "data", "Data directory")
	flag.Parse()
	
	log.Printf("Starting server on %s using data dir %s", *addr, *data)
	if err := server.Run(*addr, *data); err != nil {
		log.Fatal(err)
	}
}
