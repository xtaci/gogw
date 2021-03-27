package aiohttp

import (
	"bytes"
	"io"
	"log"
	"net/http"
)

type AIOHttpContext struct {
	state         int
	buf           *bytes.Buffer
	req           *http.Request
	contentLength int64
}

type BodyReadCloser struct {
	limitedReader *io.LimitedReader
}

func newBodyReadCloser(R io.Reader, N int64) *BodyReadCloser {
	body := new(BodyReadCloser)
	body.limitedReader = &io.LimitedReader{R, N}
	return body
}

func (body *BodyReadCloser) Read(p []byte) (n int, err error) {
	return body.limitedReader.Read(p)
}

func (body *BodyReadCloser) Close() error {
	return nil
}

type response struct {
}

func (r *response) Header() http.Header {
	return nil
}

func (r *response) Write(bts []byte) (int, error) {
	log.Println("Response", string(bts))
	return len(bts), nil
}

func (r *response) WriteHeader(statusCode int) {
}
