package main

import (
	"errors"
	"log"
	"net/http"
	_ "net/http/pprof"

	"github.com/xtaci/aiohttp"
)

var (
	errPath = errors.New("incorrect path")
)

func handler(ctx *aiohttp.AIOHttpContext) error {
	// parse URI
	var URI aiohttp.URI // current incoming request's URL
	err := URI.Parse(nil, ctx.Header.RequestURI())
	if err != nil {
		return err
	}

	// http route
	switch string(URI.Path()) {
	case "/":
		ctx.Response.SetStatusCode(200)
		ctx.ResponseData = []byte("AIOHTTP")
	default:
		ctx.Response.SetStatusCode(404)
		ctx.ResponseData = []byte("Not Found")

		return errPath
	}
	return nil
}

func main() {
	const numServer = 4
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	for i := 0; i < numServer; i++ {
		server, err := aiohttp.NewServer(":8080", 256*1024*1024, handler)
		if err != nil {
			panic(err)
		}
		server.SetLoopAffinity(i * 2)
		server.SetPoolerAffinity(i * 2)
		go server.ListenAndServe()
	}

	select {}
}
