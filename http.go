package main

import (
	"log"
	"net"

	"github.com/xtaci/gaio"
)

type AIOHttpContext struct {
	watcher *gaio.Watcher
}

// Create processor context
func NewAIOHttpProcessor(watcher *gaio.Watcher) *AIOHttpContext {
	context := new(AIOHttpContext)
	context.watcher = watcher
	return context
}

// Add connection to this processor
func (context *AIOHttpContext) AddConn(conn net.Conn) (err error) {
	err = context.watcher.Read(nil, conn, make([]byte, 128))
	if err != nil {
		return err
	}
	return nil
}

// Processor loop
func (context *AIOHttpContext) Processor() {
	for {
		// loop wait for any IO events
		results, err := context.watcher.WaitIO()
		if err != nil {
			log.Println(err)
			return
		}

		for _, res := range results {
			switch res.Operation {
			case gaio.OpRead: // read completion event
				if res.Error == nil {
					// send back everything, we won't start to read again until write completes.
					// submit an async write request
					context.watcher.Write(nil, res.Conn, res.Buffer[:res.Size])
				}
			case gaio.OpWrite: // write completion event
				if res.Error == nil {
					// since write has completed, let's start read on this conn again
					context.watcher.Read(nil, res.Conn, res.Buffer[:cap(res.Buffer)])
				}
			}
		}
	}
}
