package aiohttp

import (
	"net"

	"github.com/xtaci/gaio"
)

type Server struct {
	// addr optionally specifies the TCP address for the server to listen on,
	// in the form "host:port". If empty, ":http" (port 80) is used.
	// The service names are defined in RFC 6335 and assigned by IANA.
	// See net.Dial for details of the address format.
	addr string
	proc *AIOHttpProcessor // I/O processor
}

func ListenAndServe(addr string) error {
	watcher, err := gaio.NewWatcher()
	if err != nil {
		return err
	}
	proc := NewAIOHttpProcessor(watcher)
	server := &Server{addr: addr, proc: proc}
	return server.ListenAndServe()
}

func (srv *Server) ListenAndServe() error {
	addr := srv.addr
	if addr == "" {
		addr = ":http"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	// start processor loop
	go srv.proc.Processor()

	return srv.Serve(ln)
}

func (srv *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}

		err = srv.proc.AddConn(conn)
		if err != nil {
			return err
		}
	}
}
