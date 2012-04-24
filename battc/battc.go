package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
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

var doneJobs = make(chan string)

func handler() {
	jobs := make(map[string]*Job)
	for {
		var m batt.Message
		select {
		case <-time.After(nopDelay):
			out <- batt.Message{Verb: "nop"}
			continue
		case h := <-doneJobs:
			delete(jobs, h)
			continue
		case m = <-in:
		}
		log.Println("Message received:", m)

		switch m.Verb {
		case "build":
			h := m.Get("h")
			j := NewJob(h)
			jobs[h] = j
			go j.Build(m.Get("path"))
		case "accept":
			h := m.Get("h")
			j, ok := jobs[h]
			if !ok {
				log.Printf("unknown job %q", h)
				break
			}
			go j.Accept(m.Get("url"))
		case "nop":
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

func (j *Job) status(msg string) {
	out <- batt.Message{"status", url.Values{
		"h": []string{j.h}, "text": []string{msg},
	}}
}

func (j *Job) Build(path string) {
	j.status("starting")
	result := batt.Message{"result", url.Values{"h": []string{j.h}}}
	build := func() error {
		// create virgin environment
		gopath, err := ioutil.TempDir("", "battc")
		if err != nil {
			return err
		}
		//	defer os.RemoveAll(gopath)
		log.Println(gopath)

		// get and install package
		j.status("fetching and building")
		cmd := exec.Command("go", "get", path)
		cmd.Env = env(gopath)
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v\n%s", err, b)
		}

		j.status("finding binary")
		bindir := filepath.Join(gopath, "bin")
		dir, err := os.Open(bindir)
		if err != nil {
			return err
		}
		defer dir.Close()
		fis, err := dir.Readdir(0)
		if err != nil {
			return err
		}
		if len(fis) < 1 {
			return errors.New("couldn't find binary")
		}
		bin := filepath.Join(bindir, fis[0].Name())

		j.status("hashing")
		h, err := batt.ReadFileSHA1(bin)
		if err != nil {
			return err
		}

		// copy to tempfile outside gopath
		j.status("storing file")
		r, err := os.Open(bin)
		if err != nil {
			return err
		}
		defer r.Close()
		f, err := ioutil.TempFile("", h)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, r)
		if err != nil {
			return err
		}
		j.tmpfile = f.Name()
		j.filename = filepath.Base(bin)
		result.Set("filename", j.filename)
		result.Set("sha1", h)
		return nil
	}
	if err := build(); err != nil {
		result.Set("err", err.Error())
	}
	log.Println(j.tmpfile, result)
	out <- result
}

func (j *Job) Accept(uploadUrl string) {
	defer os.Remove(j.tmpfile)
	j.status("uploading")
	doneJobs <- j.h
}

func env(gopath string) []string {
	s := os.Environ()
	for i := len(s) - 1; i >= 0; i-- {
		switch {
		case strings.HasPrefix(s[i], "GOPATH="):
			s[i] = "GOPATH=" + gopath
		case strings.HasPrefix(s[i], "GOBIN="):
			s[i] = s[len(s)-1]
			s = s[:len(s)-1]
		}
	}
	return s
}
