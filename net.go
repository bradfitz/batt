package batt

import (
	"bufio"
	"fmt"
	"net"
)

func NewConn() *Conn {
	c := &Conn{in: make(chan Message), out: make(chan Message)}
	c.In, c.Out = c.in, c.out
	return c
}

type Conn struct {
	In      <-chan Message
	Out     chan<- Message
	in, out chan Message
}

func (c *Conn) Do(nc net.Conn) error {
	errc := make(chan error, 1)
	done := make(chan bool, 1)
	go func() {
		r := bufio.NewReader(nc)
		for {
			b, err := r.ReadBytes('\n')
			if err != nil {
				errc <- err
				return
			}
			m, err := ParseMessage(string(b))
			if err != nil {
				errc <- err
				return
			}
			select {
			case c.in <- m:
			case <-done:
				return
			}
		}

	}()
	go func() {
		for {
			var m Message
			select {
			case m = <-c.out:
			case <-done:
				return
			}
			_, err := fmt.Fprintln(nc, m)
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	err := <-errc
	done <- true
	nc.Close()
	return err
}
