package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

		log.Println("Message received:", m)
		switch m.Verb {
		case "nop":
		case "build":
			j := NewJob(m)
			jobs[j.h] = j
			go j.Build()
		case "accept":
		default:
			log.Printf("unknown verb %q", m.Verb)
		}
	}
}

func NewJob(m batt.Message) *Job {
	return &Job{
		h:    m.Get("h"),
		path: m.Get("path"),
		in:   make(chan batt.Message),
	}
}

type Job struct {
	h, path string
	in      chan batt.Message
}

func (j *Job) Build() {
	status := func(msg string) {
		out <- batt.Message{"status", url.Values{
			"h": []string{j.h}, "text": []string{j.path},
		}}
	}
	result := batt.Message{"result", url.Values{"h": []string{j.h}}}
	build := func() error {
		status("starting")

		// create virgin environment
		gopath, err := ioutil.TempDir("", "battc")
		if err != nil {
			return err
		}
		defer os.RemoveAll(gopath)

		// get and install package
		status("fetching")
		cmd := exec.Command("go", "get", j.path)
		cmd.Env = []string{"GOPATH=" + gopath}
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v\n%s", b)
		}

		// find and upload binary
		status("uploading")
		m, err := filepath.Glob(filepath.Join(gopath, "bin"))
		if err != nil {
			return err
		}
		if len(m) < 1 {
			return errors.New("couldn't find binary")
		}
		bin := m[0]
		h, err := batt.ReadFileSHA1(bin)
		if err != nil {
			return err
		}
		result.Set("sha1", h)
		result.Set("filename", bin)
		return nil
	}
	if err := build(); err != nil {
		result.Set("err", err.Error())
	}
	out <- result
}
