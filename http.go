package main

import (
	"bytes"
	"log"
	"net"

	"github.com/xtaci/gaio"
)

const (
	stateRequest = iota
	stateBody
)

type AIOHttpContext struct {
	state int
	buf   bytes.Buffer
	line  []byte
}

type AIOHttpProcessor struct {
	watcher *gaio.Watcher
}

// Create processor context
func NewAIOHttpProcessor(watcher *gaio.Watcher) *AIOHttpProcessor {
	context := new(AIOHttpProcessor)
	context.watcher = watcher
	return context
}

// Add connection to this processor
func (proc *AIOHttpProcessor) AddConn(conn net.Conn) (err error) {
	ctx := new(AIOHttpContext)
	err = proc.watcher.Read(ctx, conn, make([]byte, 128))
	if err != nil {
		return err
	}
	return nil
}

// Processor loop
func (proc *AIOHttpProcessor) Processor() {
	for {
		// loop wait for any IO events
		results, err := proc.watcher.WaitIO()
		if err != nil {
			log.Println(err)
			return
		}

		for _, res := range results {
			switch res.Operation {
			case gaio.OpRead: // read completion event
				if res.Error == nil {
					proc.processRequest(&res)
					// send back everything, we won't start to read again until write completes.
					// submit an async write request
					proc.watcher.Write(nil, res.Conn, res.Buffer[:res.Size])
				}
			case gaio.OpWrite: // write completion event
				if res.Error == nil {
					// since write has completed, let's start read on this conn again
					proc.watcher.Read(nil, res.Conn, res.Buffer[:cap(res.Buffer)])
				}
			}
		}
	}
}

// process request
func (context *AIOHttpProcessor) processRequest(res *gaio.OpResult) {
	const (
		stateBeginLine = iota // beginning of line; initial state; must be zero
		stateCR               // read \r (possibly at end of line)
		stateData             // reading data in middle of line
	)

	ctx := res.Context.(*AIOHttpContext)

	bufferIdx := 0
	for bufferIdx < res.Size {
		c := res.Buffer[bufferIdx]
		bufferIdx++

		switch ctx.state {
		case stateBeginLine:
			ctx.state = stateData

		case stateCR:
			if c == '\n' {
				ctx.state = stateBeginLine
				//  TODO: process the new line
			} else {
				// Not part of \r\n. Emit saved \r
				bufferIdx--
				c = '\r'
				ctx.state = stateData
			}

		case stateData:
			if c == '\r' {
				ctx.state = stateCR
				continue
			}
			if c == '\n' {
				ctx.state = stateBeginLine
			}
		}
		ctx.line = append(ctx.line, c)
	}
	return
}
