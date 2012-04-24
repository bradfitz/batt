package main

import (
	"bytes"
	"crypto/rand"
	"crypto/hmac"
	"crypto/sha1"
	"errors"
	"flag"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/bradfitz/batt"
)

var (
	webListen = flag.String("web", ":8082", "web listen address")
	tcpListen = flag.String("tcp", ":9999", "TCP listen address")
	cacheDir  = flag.String("cachedir", "/tmp", "cache dir")
	baseURL   = flag.String("baseurl", "http://gophorge.com", "base URL")
)

var homeTemplate = template.Must(template.New("home").Parse(`
<html>
  <head>
    <title>gophorge</title>
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
    <h1>Gophorge</h1>
    <h2>build *all* the things!</h2>
    <p>This is a build tool for <a href="http://golang.org/">Go</a> programs. Workers are connected on many platforms and will build executables of any public Go command packages.</p>
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
        <th>Disclaimer:</th>
	<td>
	<input name='disclaimed' type='checkbox' value='ok' id='legalcheck'>
	<label for='legalcheck'><i>I acknowledge that gophorge has no control over the input source code and that the resulting binaries could do anything: wipe your files, destroy your computer, send spam, etc.  The builders might even be compromised and put viruses in the executables, even if the source code is harmless.  Gophorge provides these binaries as-is, with no warranty or guarantees of any kind. Use at your own risk!</i></label>
	</td>
      </tr>
      <tr>
        <th></th><td><input type='submit' value='Build'></td>
      </tr>
      </table>
    </form>
    <p><hr><small>quick hack by adg and bradfitz. no warranties. use at your own risk. source code is <a href='https://github.com/bradfitz/batt/'>here</a>.</small></p>
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
	if r.FormValue("disclaimed") != "ok" {
		http.Error(rw, "You must accept the disclaimer to use this service.", 402)
		return
	}

	p := r.FormValue("platform")
	w, ok := workerForPlatform(p)
	if !ok {
		http.Error(rw, "invalid platform, or no connected workers", 500)
		return
	}

	rc, filename, err := w.Build(r.FormValue("pkg"), p)
	if err != nil {
		rw.Header().Set("Content-Type", "text/html; charset-utf-8")
		rw.WriteHeader(500)
		fmt.Fprintf(rw, "<html><body>Error building:<pre>"+html.EscapeString(err.Error())+"</pre></body></html>")
		return
	}
	defer rc.Close()
	rw.Header().Set("Content-Type", "application/octet-stream")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+url.QueryEscape(filename)+`"`)
	io.Copy(rw, rc)
}

const maxBinarySize = 32 << 20

// accept a binary
func accept(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		http.Error(rw, "not a PUT", 400)
		return
	}
	q := r.URL.Query()
	size, err := strconv.Atoi(q.Get("size"))
	if err != nil || size < 1 || size > maxBinarySize {
		http.Error(rw, "bad size", 400)
		return
	}

	qsha1 := q.Get("sha1")
	if !validSHA1.MatchString(qsha1) {
		http.Error(rw, "bad sha1", 400)
		return
	}
	if q.Get("k") != hmacSHA1(qsha1) {
		http.Error(rw, "bad hmac key", 400)
		return
	}

	var buf bytes.Buffer
	s1 := sha1.New()
	n, err := io.Copy(io.MultiWriter(&buf, s1), io.LimitReader(r.Body, int64(size)))
	if err != nil {
		log.Printf("copy error: %v", err)
		http.Error(rw, "copy error", 400)
		return
	}
	if n != int64(size) {
		http.Error(rw, "bad size", 400)
		return
	}
	if fmt.Sprintf("%x", s1.Sum(nil)) != qsha1 {
		http.Error(rw, "bad sha1", 400)
		return
	}

	filename := filepath.Join(*cacheDir, qsha1+".battd")
	err = ioutil.WriteFile(filename, buf.Bytes(), 0644)
	if err != nil {
		log.Printf("WriteFile: %v", err)
		http.Error(rw, "fs write error", 500)
		return
	}

	// Run callbacks.
	mu.Lock()
	defer mu.Unlock()
	for _, fn := range sha1Sub[qsha1] {
		fn()
	}
	delete(sha1Sub, qsha1)

	rw.WriteHeader(204)
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
	mu      sync.Mutex                      // guards registry maps below
	workers = map[string]map[*Worker]bool{} // platform ("linux-amd64") -> set of workers
	sha1Sub = map[string][]func(){}         // sha1 -> callbacks
)

func registerSHA1Callback(s string, fn func()) {
	mu.Lock()
	defer mu.Unlock()
	sha1Sub[s] = append(sha1Sub[s], fn)
}

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

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, b)
	if err != nil {
		panic(err)
	}
	return b
}

func newHandle() string {
	return fmt.Sprintf("%x", randomBytes(16))
}

type BuildRequest struct {
	Handle   string // random opaque
	Package  string
	Platform string
	Res      chan BuildResult
}

type BuildResult struct {
	io.ReadCloser
	Filename string
	Error    error
}

type Worker struct {
	Addr      string
	Platforms []string // "linux-amd64"
	Conn      *batt.Conn
	in        chan interface{}
}

func (w *Worker) String() string {
	return fmt.Sprintf("[Worker %v: %+v]", w.Addr, w.Platforms)
}

func (w *Worker) Build(pkg, platform string) (io.ReadCloser, string, error) {
	br := &BuildRequest{
		Handle:   newHandle(),
		Package:  pkg,
		Platform: platform,
		Res:      make(chan BuildResult, 1),
	}
	w.in <- br
	r := <-br.Res
	if r.Error != nil {
		return nil, "", r.Error
	}
	return r, r.Filename, nil
}

func (w *Worker) loop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	outstanding := map[string]*BuildRequest{} // keyed by BuildRequest.Handle

	for {
		select {
		case <-ticker.C:
			w.Conn.Write(batt.Message{Verb: "nop"})
		case mi, ok := <-w.in:
			if !ok {
				return
			}
			switch m := mi.(type) {
			case func():
				m()
			case batt.Message:
				log.Printf("Message from %s: %+v", w, m)
				switch m.Verb {
				case "nop":
					// Nothing.
					continue
				case "status":
					br, ok := outstanding[m.Get("h")]
					if ok {
						log.Printf("Worker status for %v: %s", br, m.Get("text"))
					}
				case "result":
					handle := m.Get("h")
					br, ok := outstanding[handle]
					if !ok {
						return
					}
					errText := m.Get("err")
					if errText != "" {
						delete(outstanding, handle)
						br.Res <- BuildResult{Error: errors.New(errText)}
						continue
					}
					sha := m.Get("sha1")
					rc, ok := findCachedSHA1(sha)
					if ok {
						delete(outstanding, handle)
						br.Res <- BuildResult{rc, m.Get("filename"), nil}
						return
					}

					registerSHA1Callback(sha, func() {
						w.in <- func() {
							delete(outstanding, handle)
							rc, ok := findCachedSHA1(sha)
							if !ok {
								br.Res <- BuildResult{Error: errors.New("missing expected sha1 file")}
								return
							}
							br.Res <- BuildResult{rc, m.Get("filename"), nil}
						}
					})
					acceptURL := *baseURL + "/accept?size=" + m.Get("size") + "&sha1=" + sha + "&k=" + hmacSHA1(sha)
					w.Conn.Write(batt.Message{
						Verb: "accept",
						Values: url.Values{
							"h":   []string{br.Handle},
							"url": []string{acceptURL},
						}})
				default:
					log.Printf("Unknown message: %v", batt.Message{Verb: "error", Values: url.Values{"text": []string{"Unknown verb " + m.Verb}}})
				}
			case *BuildRequest:
				br := m
				outstanding[br.Handle] = br
				brm := batt.Message{
					Verb: "build",
					Values: url.Values{
						"h":        []string{br.Handle},
						"platform": []string{br.Platform},
						"path":     []string{br.Package},
					},
				}
				log.Printf("Sending build request message: %v", brm)
				w.Conn.Write(brm)
			}
		}
	}
}

var serverKey = randomBytes(128)

func hmacSHA1(in string) string {
	h := hmac.New(sha1.New, serverKey)
	io.WriteString(h, in)
	return fmt.Sprintf("%x", h.Sum(nil))
}

var validSHA1 = regexp.MustCompile(`^[0-9a-f]{40,40}$`)

func findCachedSHA1(sha1 string) (io.ReadCloser, bool) {
	if !validSHA1.MatchString(sha1) {
		return nil, false
	}
	f, err := os.Open(filepath.Join(*cacheDir, sha1+".battd"))
	if err == nil {
		return f, true
	}
	return nil, false
}

func main() {
	batt.Init()

	http.HandleFunc("/", home)
	http.HandleFunc("/accept", accept)
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
