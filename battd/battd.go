package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

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
		go handleWorkerConn(c)
	}
}

func handleWorkerConn(nc net.Conn) {
	defer nc.Close()
	addr := nc.RemoteAddr()

	boot := make(chan string, 1)
	defer func() {
		var reason string
		select {
		case reason = <-boot:
		default:
		}
		log.Printf("Worker conn %s disconnected; reason=%v", addr, reason)
	}()

	log.Printf("Got potential worker connection from %s", addr)

	// They get 5 seconds to authenticate.
	authTimer := time.AfterFunc(5*time.Second, func() {
		boot <- "login timeout"
		nc.Close()
	})

	c := batt.NewConn(nc)
	m, err := c.Read()
	if err != nil {
		boot <- fmt.Sprintf("inital message read error: %v", err)
		return
	}
	if m.Verb != "hello" {
		boot <- "speaking while not authenticated"
		return
	}
	if batt.Secret == "" || m.Get("k") != batt.Secret {
		boot <- fmt.Sprintf("bad password; want %q", batt.Secret)
		return
	}
	authTimer.Stop()

	platforms := m.Values["p"]
	log.Printf("Worker conn %s authenticated; working for clients: %+v", addr, platforms)
	c.Write(batt.Message{Verb: "hello"})

	w := &Worker{
		Addr:      addr.String(),
		Platforms: platforms,
		Conn:      c,
		in:        make(chan interface{}),
	}
	registerWorker(w)
	defer unregisterWorker(w)
	defer close(w.in)

	go w.loop()
	for {
		m, err := c.Read()
		if err != nil {
			boot <- fmt.Sprintf("message read error: %v", err)
			log.Printf("Worker conn %v shut down: %v", addr, err)
			return
		}
		w.in <- m
	}
	panic("unreachable")
}

var (
	mu      sync.Mutex
	workers = map[string]map[*Worker]bool{} // platform ("linux-amd64") -> set of workers
)

func registerWorker(w *Worker) {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range w.Platforms {
		if _, ok := workers[p]; !ok {
			workers[p] = make(map[*Worker]bool)
		}
		workers[p][w] = true
	}
}

func unregisterWorker(w *Worker) {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range w.Platforms {
		delete(workers[p], w)
	}
}

type Worker struct {
	Addr      string
	Platforms []string // "linux-amd64"
	Conn      *batt.Conn
	in        chan interface{}
}

func (w *Worker) loop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.Conn.Write(batt.Message{Verb: "nop"})
		case mi, ok := <-w.in:
			if !ok {
				return
			}
			switch m := mi.(type) {
			case batt.Message:
				log.Printf("Message from %s: %+v", w.Addr, m)
				switch m.Verb {
				case "nop":
					// Nothing.
					continue
				default:
					w.Conn.Write(batt.Message{Verb: "error", Values: url.Values{"text": []string{"Unknown verb " + m.Verb}}})
				}
			}
		}
	}

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
