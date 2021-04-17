package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"

	"github.com/xtaci/aiohttp"
)

func main() {
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	aiohttp.ListenAndServe(":8080", 4, 256*1024*1024)
}
