package aiohttp

import (
	"log"
	"net"

	"github.com/xtaci/gaio"
)

func ListenAndServe(addr string, numServer int, bufSize int) error {
	if addr == "" {
		addr = ":http"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	var processors []*AIOHttpProcessor
	for i := 0; i < numServer; i++ {
		watcher, err := gaio.NewWatcherSize(bufSize)
		if err != nil {
			return err
		}
		proc := NewAIOHttpProcessor(watcher)
		proc.StartProcessor()
		processors = append(processors, proc)
		log.Println(i)
	}

	// start processor loop
	r := 0
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}

		err = processors[r].AddConn(conn)
		if err != nil {
			return err
		}
		r = (r + 1) % numServer
	}
}
