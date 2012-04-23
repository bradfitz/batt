package batt

import (
	"fmt"
	"net/url"
	"crypto/sha1"
	"io"
	"os"
)

type Message struct {
	Verb string
	url.Values
}

func (m Message) String() string {
	return fmt.Sprintf("%v %v", m.Verb, m.Values)
}

func ParseMessage(s string) (m Message, err error) {
	var v string
	var n int
	n, err = fmt.Sscan(s, &m.Verb, &v)
	if err != nil && n != 1 {
		if n == 1 {
			// ok to scan verb only
			err = nil
		}
		return
	}
	m.Values, err = url.ParseQuery(v)
	return
}

func ReadFileSHA1(filename string) (string, error) {
	r, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha1.New()
	_, err = io.Copy(h, r)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil

}
