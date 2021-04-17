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

	for i := 0; i < 4; i++ {
		go aiohttp.ListenAndServe(":8080", i, 256*1024*1024)
	}

	select {}
}
