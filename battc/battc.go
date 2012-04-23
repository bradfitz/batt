package main

import (
	"flag"
	"log"
	"net"
	"net/url"
	"time"

	"github.com/bradfitz/batt"
)

var serverAddr = flag.String("server", "zon.danga.com:9999", "server address:port")

const nopDelay = time.Second * 10

func main() {
	batt.Init()
	platforms := flag.Args()

	go handler()

	backoff := 0
	for {
		backoff <<= 1
		nc, err := net.Dial("tcp", *serverAddr)
		if err != nil {
			log.Println("Dial:", err)
			continue
		}
		c := batt.NewConn(nc)
		err = c.Write(batt.Message{"hello", url.Values{
			"k": []string{batt.Secret},
			"p": platforms,
		}})
		if err != nil {
			log.Println("Hello:", err)
			continue
		}
		err = handle(c)
		log.Println(err)
	}
}

var in, out = make(chan batt.Message), make(chan batt.Message)

func handle(c *batt.Conn) error {
	errc := make(chan error, 1)
	done := make(chan bool, 1)
	go func() {
		for {
			m, err := c.Read()
			if err != nil {
				errc <- err
				return
			}
			select {
			case in <- m:
			case <-done:
				return
			}
		}

	}()
	go func() {
		for {
			var m batt.Message
			select {
			case m = <-out:
			case <-done:
				return
			}
			if err := c.Write(m); err != nil {
				errc <- err
				return
			}
		}
	}()
	err := <-errc
	done <- true
	c.Close()
	return err
}

func handler() {
	for {
		var m batt.Message
		select {
		case <-time.After(nopDelay):
			out <- batt.Message{Verb: "nop"}
			continue
		case m = <-in:
		}
		log.Println("Message received:", m)
	}
}
