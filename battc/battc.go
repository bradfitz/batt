package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bradfitz/batt"
)

var (
	serverAddr = flag.String("server", "gophorge.com:9999", "server address:port")
	platforms  []string
)

const nopDelay = time.Second * 10

func main() {
	batt.Init()
	platforms = flag.Args()

	go handler()

	for {
		// TODO(adg): backoff
		log.Println(connect(*serverAddr))
		time.Sleep(10 * time.Second)
	}
}

var in, out = make(chan batt.Message), make(chan batt.Message)

func connect(addr string) error {
	nc, err := net.Dial("tcp", *serverAddr)
	if err != nil {
		return err
	}
	defer nc.Close()
	c := batt.NewConn(nc)
	err = c.Write(batt.Message{"hello", url.Values{
		"k": []string{batt.Secret},
		"p": platforms,
	}})
	if err != nil {
		return err
	}
	m, err := c.Read()
	if err != nil {
		return err
	}
	if m.Verb != "hello" {
		return fmt.Errorf(`expected "hello", got %q`, m.Verb)
	}
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
	err = <-errc
	done <- true // shut down other goroutine
	return err
}

func handler() {
	jobs := make(map[string]*Job)
	for {
		var m batt.Message
		select {
		case <-time.After(nopDelay):
			out <- batt.Message{Verb: "nop"}
			continue
		case m = <-in:
		}
		if m.Verb == "nop" {
			continue // ignore
		}
		log.Println("received:", m)

		switch m.Verb {
		case "build":
			h := m.Get("h")
			j := NewJob(h)
			jobs[h] = j
			go j.Build(m.Get("path"), m.Get("platform"))
		case "accept":
			h := m.Get("h")
			j, ok := jobs[h]
			if !ok {
				log.Printf("unknown job %q", h)
				break
			}
			delete(jobs, h)
			go j.Accept(m.Get("url"))
		default:
			log.Printf("unknown verb %q", m.Verb)
		}
	}
}

func NewJob(h string) *Job {
	return &Job{h: h}
}

type Job struct {
	h        string
	tmpfile  string // temporary location of build result
	filename string
}

func (j *Job) logf(format string, args ...interface{}) {
	s := fmt.Sprintf(format, args...)
	log.Printf("Job %s: %s", j.h, s)
}

func (j *Job) status(msg string) {
	j.logf("%s", msg)
	out <- batt.Message{"status", url.Values{
		"h": []string{j.h}, "text": []string{msg},
	}}
}

func (j *Job) Build(path, platform string) {
	j.status("starting")
	result := batt.Message{"result", url.Values{"h": []string{j.h}}}
	build := func() error {
		// create virgin environment
		gopath, err := ioutil.TempDir("", "battc")
		if err != nil {
			return err
		}
		defer os.RemoveAll(gopath)

		j.status("fetching and building")
		cmd := exec.Command("go", "get", path)
		cmd.Env = env(gopath, platform)
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go get: %v\n%s", err, b)
		}

		j.status("finding binary")
		bindir := filepath.Join(gopath, "bin")
		bin, size, err := findFile(bindir)
		if err != nil {
			return fmt.Errorf("finding: %v", err)
		}
		j.filename = filepath.Base(bin)
		result.Set("filename", j.filename)
		result.Set("size", fmt.Sprint(size))

		j.status("hashing")
		h, err := batt.ReadFileSHA1(bin)
		if err != nil {
			return fmt.Errorf("hashing: %v", err)
		}
		result.Set("sha1", h)

		j.status("storing file")
		tmpfile, err := cpToTempFile(bin, h)
		if err != nil {
			return fmt.Errorf("storing: %v", err)
		}
		j.tmpfile = tmpfile

		return nil
	}
	if err := build(); err != nil {
		j.logf("build: %v", err)
		result.Set("err", err.Error())
	}
	if j.tmpfile != "" {
		j.logf("tmpfile: %s", j.tmpfile)
	}
	j.logf("result: %v", result)
	out <- result

	j.status("waiting for accept")
}

func (j *Job) Accept(uploadUrl string) {
	defer j.status("done")
	defer os.Remove(j.tmpfile)

	j.status("uploading")
	f, err := os.Open(j.tmpfile)
	if err != nil {
		j.logf("%v", err)
		return
	}
	defer f.Close()
	req, err := http.NewRequest("PUT", uploadUrl, f)
	if err != nil {
		j.logf("NeqRequest: %v", err)
		return
	}
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		j.logf("RoundTrip: %v", err)
		return
	}
	if res.StatusCode/100 != 2 {
		j.logf("HTTP Response: %v", res.Status)
	}
}

func env(gopath, platform string) []string {
	p := strings.SplitN(platform, "-", 2)
	var goos, goarch string
	if len(p) > 1 {
		goos, goarch = p[0], p[1]
	}
	s := os.Environ()
	for i := len(s) - 1; i >= 0; i-- {
		switch {
		case strings.HasPrefix(s[i], "GOARCH="):
		case strings.HasPrefix(s[i], "GOBIN="):
		case strings.HasPrefix(s[i], "GOOS="):
		case strings.HasPrefix(s[i], "GOPATH="):
		default:
			continue
		}
		s[i] = s[len(s)-1]
		s = s[:len(s)-1]
	}
	if goos != "" {
		s = append(s, "GOOS="+goos)
	}
	if goarch != "" {
		s = append(s, "GOARCH="+goarch)
	}
	return append(s, "GOPATH="+gopath)
}

func cpToTempFile(filename, tmpfilename string) (tmpfile string, err error) {
	r, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer r.Close()
	f, err := ioutil.TempFile("", tmpfilename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	if err != nil {
		return "", err
	}
	return f.Name(), nil
}

func findFile(dir string) (filename string, size int64, err error) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	fis, err := d.Readdir(0)
	if err != nil {
		return
	}
	if len(fis) < 1 {
		err = errors.New("couldn't find file")
		return
	}
	if fis[0].IsDir() {
		// recurse into directories
		return findFile(filepath.Join(dir, fis[0].Name()))
	}
	return filepath.Join(dir, fis[0].Name()), fis[0].Size(), nil
}
