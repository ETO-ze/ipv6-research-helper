package main

import (
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type tunnelHTTPConn struct {
	net.Conn
	reader io.Reader
	done   chan struct{}
	once   sync.Once
}

func (c *tunnelHTTPConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
func (c *tunnelHTTPConn) Close() error               { c.once.Do(func() { close(c.done) }); return c.Conn.Close() }

type oneListener struct {
	c    *tunnelHTTPConn
	sent bool
}

func (l *oneListener) Accept() (net.Conn, error) {
	if !l.sent {
		l.sent = true
		return l.c, nil
	}
	<-l.c.done
	return nil, net.ErrClosed
}
func (l *oneListener) Close() error   { return l.c.Close() }
func (l *oneListener) Addr() net.Addr { return l.c.LocalAddr() }
func (a *App) connectHTTP(w http.ResponseWriter, r *http.Request, host string) {
	h, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	c, rw, e := h.Hijack()
	if e != nil {
		return
	}
	rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if rw.Flush() != nil {
		c.Close()
		return
	}
	conn := &tunnelHTTPConn{Conn: c, reader: rw, done: make(chan struct{})}
	l := &oneListener{c: conn}
	defer l.Close()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dest := r.Host
		if h, _, e := net.SplitHostPort(dest); e == nil {
			dest = h
		}
		if norm(dest) != host || r.Method == "CONNECT" {
			http.Error(w, "Tunnel destination mismatch", 403)
			return
		}
		a.proxy(w, r)
	})
	(&http.Server{Handler: handler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 65536}).Serve(l)
}
