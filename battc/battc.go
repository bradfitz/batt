package main

import (
	"flag"
	"net"
	"log"
	"time"

	"github.com/bradfitz/batt"
)


var serverAddr = flag.String("server", "zon.danga.com:9999", "server address:port")

const nopDelay = time.Second * 10

var conn = batt.NewConn()

func main() {
	batt.Init()

	go handler()

	for {
		nc, err := net.Dial("tcp", *serverAddr)
		if err != nil {
			log.Println("Dial:", err)
			continue
		}
		err = conn.Do(nc)
		log.Println(err)
	}
}

func handler() {
	for {
		var m batt.Message
		select {
		case <-time.After(nopDelay):
			conn.Out <- batt.Message{Verb: "nop"}
			continue
		case m = <-conn.In:
		}
		log.Println("Message received:", m)
	}
}
