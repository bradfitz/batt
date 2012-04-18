package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var (
	webListen  = flag.String("web", ":8080", "web listen address")
	tcpListen  = flag.String("tcp", ":9999", "TCP listen address")
	secretFile = flag.String("secretfile", filepath.Join(os.Getenv("HOME"), ".batt-secret"), "filename of secret")
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

func runWorker(c net.Conn) {
	log.Printf("Got potential worker connection from %s", c.RemoteAddr())
	// TODO
	c.Close()
}

func main() {
	flag.Parse()

	slurp, err := ioutil.ReadFile(*secretFile)
	if err != nil {
		log.Fatalf("Error reading secret file: %v", err)
	}
	secret := strings.TrimSpace(string(slurp))
	_ = secret

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
