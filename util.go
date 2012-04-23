package batt

import (
	"fmt"
	"net/url"
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
