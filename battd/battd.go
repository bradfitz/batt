package main

import (
	"flag"
	"log"
	"net/http"
)

var (
	listen = flag.String("listen", ":8080", "address to listen on")
)

func main() {
	flag.Parse()
	
	log.Printf("Listening on %s", *listen)
	if err := http.ListenAndServe(*listen, nil); err != nil {
		log.Fatalf("error: %v", err)
	}
}


