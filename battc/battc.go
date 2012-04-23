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
	serverAddr = flag.String("server", "zon.danga.com:9999", "server address:port")
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
		case "build":
			// TODO(adg): validate input
			go build(m.Get("h"), m.Get("path"))
		case "accept":
		default:
			log.Println("Unknown verb: %v", m.Verb)
		}
	}
}

func build(h, path string) {
	status := func(msg string) {
		out <- batt.Message{"status", url.Values{
			"h": []string{h}, "text": []string{msg},
		}}
	}
	do := func() error {
		status("starting")

		// create virgin environment
		gopath, err := ioutil.TempDir("", "battc")
		if err != nil {
			return err
		}
		defer os.RemoveAll(gopath)

		// get and install package
		status("fetching")
		cmd := exec.Command("go", "get", path)
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
		sha1, err := batt.ReadFileSHA1(bin)
		if err != nil {
			return err
		}
		out <- batt.Message{"built", url.Values{
			"h":        []string{h},
			"sha1":     []string{sha1},
			"filename": []string{filepath.Base(bin)},
		}}
		return nil
	}
	if err := do(); err != nil {
		status(err.Error())
	}
}
