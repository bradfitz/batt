package batt

import (
	"bufio"
	"fmt"
	"net"
)

func NewConn(nc net.Conn) *Conn {
	c := &Conn{
		nc:  nc,
		buf: bufio.NewReader(nc),
	}
	return c
}

type Conn struct {
	nc  net.Conn
	buf *bufio.Reader
}

func (c *Conn) Read() (m Message, err error) {
	var b []byte
	b, err = c.buf.ReadBytes('\n')
	if err != nil {
		return
	}
	return ParseMessage(string(b))
}

func (c *Conn) Write(m Message) error {
	_, err := fmt.Fprintln(c.nc, m)
	return err
}

func (c *Conn) Close() error {
	return c.nc.Close()
}
