package aiohttp

import (
	"bytes"
	"log"
	"net/http"
)

type AIOHttpContext struct {
	state int
	buf   *bytes.Buffer
}

type Response struct {
}

func (r *Response) Header() http.Header {
	return nil
}

func (r *Response) Write(bts []byte) (int, error) {
	log.Println("Response", string(bts))
	return len(bts), nil
}

func (r *Response) WriteHeader(statusCode int) {
}
