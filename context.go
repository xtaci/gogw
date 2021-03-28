package aiohttp

import (
	"bytes"
	"io"
	"net/http"

	"github.com/xtaci/gaio"
)

//  AIO Http context
type AIOHttpContext struct {
	state    int
	buf      *bytes.Buffer
	xmitBuf  *bytes.Buffer
	watcher  *gaio.Watcher
	header   RequestHeader
	response ResponseHeader
}

// body defines a body reader
type body struct {
	limitedReader *io.LimitedReader
}

func newBody(R io.Reader, N int64) *body {
	b := new(body)
	b.limitedReader = &io.LimitedReader{R, N}
	return b
}

func (b *body) Read(p []byte) (n int, err error) {
	return b.limitedReader.Read(p)
}

func (*body) Close() error {
	return nil
}

// response
type response struct {
	header     http.Header
	statusCode int
	buf        *bytes.Buffer
}

func newResponse() *response {
	res := new(response)
	res.header = make(http.Header)
	res.buf = new(bytes.Buffer)
	return res
}

func (r *response) Header() http.Header {
	return r.header
}

func (r *response) Write(bts []byte) (int, error) {
	return r.buf.Write(bts)
}

func (r *response) WriteHeader(statusCode int) {
	r.statusCode = statusCode
}
