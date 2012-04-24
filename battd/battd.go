package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/bradfitz/batt"
)

var (
	webListen = flag.String("web", ":8082", "web listen address")
	tcpListen = flag.String("tcp", ":9999", "TCP listen address")
)

var homeTemplate = template.Must(template.New("home").Parse(`
<html>
  <head>
    <title>build *all* the things!</title>
  </head>
  <style>
    body {
      font-family: sans-serif;
    }
    td, th {
      vertical-align: top;
    }
    th {
      text-align: left;
    }
  </style>
  <body>
    <h1>build *all* the things!</h1>
    <form action='/build' method='post'>
      <table>
      <tr>
        <th>Platform:</th>
        <td>
	  {{with .Platforms}}
            {{range .}}
	      <label><input type="radio" name="platform" value="{{.}}"> {{.}}</label><br>
            {{end}}
	  {{else}}
	    None available; try again soon!
	  {{end}}
	</td>
      </tr>
      <tr>
        <th>Package:</th>
	<td>
	  <input name='pkg' size='100'><br>
	  <i>(eg, "github.com/nf/todo")</i>
	</td>
      </tr>
      <tr>
        <th></th><td><input type='submit' value='Build'></td>
      </tr>
      </table>
    </form>
  </body>
</html>
`))

type homeTemplateData struct {
	Platforms []string
}

func home(w http.ResponseWriter, r *http.Request) {
	err := homeTemplate.Execute(w, homeTemplateData{
		Platforms: platforms(),
	})
	if err != nil {
		log.Fatal(err)
	}
}

func build(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(rw, "not a POST", 400)
		return
	}

	p := r.FormValue("platform")
	w, ok := workerForPlatform(p)
	if !ok {
		http.Error(rw, "invalid platform, or no connected workers", 500)
		return
	}
	_ = w
	fmt.Fprintf(rw, "should build now")
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

func workerForPlatform(p string) (w *Worker, ok bool) {
	mu.Lock()
	defer mu.Unlock()
	for w := range workers[p] {
		return w, true
	}
	return nil, false
}

func platforms() (s []string) {
	mu.Lock()
	defer mu.Unlock()
	for p := range workers {
		if len(workers[p]) > 0 {
			s = append(s, p)
		}
	}
	sort.Strings(s)
	return
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
