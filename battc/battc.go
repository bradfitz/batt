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
			go j.Build(m.Get("path"))
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
		defer os.RemoveAll(gopath)

		j.status("fetching and building")
		cmd := exec.Command("go", "get", path)
		cmd.Env = env(gopath)
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v\n%s", err, b)
		}

		j.status("finding binary")
		bindir := filepath.Join(gopath, "bin")
		fi, err := findFile(bindir)
		if err != nil {
			return err
		}
		bin := filepath.Join(bindir, fi.Name())
		j.filename = fi.Name()
		result.Set("filename", j.filename)
		result.Set("size", fmt.Sprint(fi.Size()))

		j.status("hashing")
		h, err := batt.ReadFileSHA1(bin)
		if err != nil {
			return err
		}
		result.Set("sha1", h)

		j.status("storing file")
		tmpfile, err := cpToTempFile(bin, h)
		if err != nil {
			return err
		}
		j.tmpfile = tmpfile

		return nil
	}
	if err := build(); err != nil {
		result.Set("err", err.Error())
	}
	log.Println(result)
	log.Println("tmpfile:", j.tmpfile)
	out <- result

	j.status("waiting for upload url")
}

func (j *Job) Accept(uploadUrl string) {
	defer os.Remove(j.tmpfile)
	defer j.status("done")
	j.status("uploading")
	f, err := os.Open(j.tmpfile)
	if err != nil {
		log.Println(err)
		return
	}
	defer f.Close()
	req, err := http.NewRequest("PUT", uploadUrl, f)
	if err != nil {
		log.Println(err)
		return
	}
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		log.Println(err)
		return
	}
	if res.StatusCode/100 != 2 {
		log.Println("bad upload:", res.Status)
		return
	}
}

func env(gopath string) []string {
	s := os.Environ()
	for i := len(s) - 1; i >= 0; i-- {
		switch {
		case strings.HasPrefix(s[i], "GOPATH="):
		case strings.HasPrefix(s[i], "GOBIN="):
		default:
			continue
		}
		s[i] = s[len(s)-1]
		s = s[:len(s)-1]
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

func findFile(dir string) (os.FileInfo, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	fis, err := d.Readdir(0)
	if err != nil {
		return nil, err
	}
	if len(fis) < 1 {
		return nil, errors.New("couldn't find file")
	}
	return fis[0], nil
}
