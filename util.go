package batt

import (
	"fmt"
	"net/url"
)

type Message struct {
	url.Values
	Verb string
}

func (m Message) String() string {
	return fmt.Sprintf("%v %v", m.Verb, m.Values)
}

func ParseMessage(s string) (m Message, err error) {
	var v string
	_, err = fmt.Sscan(s, &m.Verb, &v)
	if err != nil {
		return
	}
	m.Values, err = url.ParseQuery(v)
	return
}
