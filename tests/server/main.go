package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"

	"github.com/xtaci/aiohttp"
)

func handler(ctx *aiohttp.AIOHttpContext) {
	switch string(ctx.URI.Path()) {
	case "/":
		ctx.Response.SetStatusCode(200)
		ctx.ResponseData = []byte("AIOHTTP")
	default:
		ctx.Response.SetStatusCode(404)
		ctx.ResponseData = []byte("Not Found")
	}
}

func main() {
	const numServer = 8
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	for i := 0; i < numServer; i++ {
		go aiohttp.ListenAndServe(":8080", i, 256*1024*1024, handler)
	}

	select {}
}
