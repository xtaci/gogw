package aiohttp

import (
	"log"
	"net"

	reuse "github.com/libp2p/go-reuseport"
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

func ListenAndServe(addr string, numServer int, bufSize int) error {
	for i := 0; i < numServer; i++ {
		watcher, err := gaio.NewWatcherSize(bufSize)
		if err != nil {
			return err
		}
		proc := NewAIOHttpProcessor(watcher)
		server := &Server{addr: addr, proc: proc}
		go server.ListenAndServe()
		log.Println(i)
	}
	select {}
	return nil
}

func (srv *Server) ListenAndServe() error {
	addr := srv.addr
	if addr == "" {
		addr = ":http"
	}

	ln, err := reuse.Listen("tcp", addr)
	if err != nil {
		return err
	}

	// start processor loop
	srv.proc.StartProcessor()

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
