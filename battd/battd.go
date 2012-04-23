package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/bradfitz/batt"
)

var (
	webListen = flag.String("web", ":8080", "web listen address")
	tcpListen = flag.String("tcp", ":9999", "TCP listen address")
)

func home(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, `<html><head>
  <title>build *all* the things!</title>
</head>
<body>
<h1>build *all* the things!</h1>
<form action='/build' method='post'>
 Package: <input name='pkg'> <input type='submit' value='build'>
</form>
</body></html>`)
}

func build(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "not a POST", 400)
		return
	}

	fmt.Fprintf(w, "should build now")
}

func acceptWorkers(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatalf("Accept: %v", err)
		}
		go runWorker(c)
	}
}

func runWorker(nc net.Conn) {
	defer nc.Close()
	addr := nc.RemoteAddr()
	log.Printf("Got potential worker connection from %s", addr)
	c := batt.NewConn(nc)
	for {
		m, err := c.Read()
		if err != nil {
			log.Printf("Worker conn %v shut down: %v", addr, err)
			return
		}
		log.Printf("Message from %s: %+v", addr, m)
	}
	panic("unreachable")
}

func main() {
	batt.Init()

	http.HandleFunc("/", home)
	http.HandleFunc("/build", build)

	log.Printf("Listening for worker connections on %s", *tcpListen)
	tln, err := net.Listen("tcp", *tcpListen)
	if err != nil {
		log.Fatalf("listen error: %v", err)
	}

	go acceptWorkers(tln)

	log.Printf("Listening for web requests on %s", *webListen)
	if err := http.ListenAndServe(*webListen, nil); err != nil {
		log.Fatalf("web listen: %v", err)
	}
}
