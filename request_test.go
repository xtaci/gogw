package aiohttp

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadRequest(t *testing.T) {

	testRequest := `GET / HTTP/1.1
Host: localhost:8080
Connection: keep-alive
Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8
User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_8_2) AppleWebKit/537.17 (KHTML, like Gecko) Chrome/24.0.1312.52 Safari/537.17
Accept-Encoding: gzip,deflate,sdch
Accept-Language: en-US,en;q=0.8
Accept-Charset: ISO-8859-1,utf-8;q=0.7,*;q=0.3
Cookie: __utma=1.1978842379.1323102373.1323102373.1323102373.1; EPi:NumberOfVisits=1,2012-02-28T13:42:18; CrmSession=5b707226b9563e1bc69084d07a107c98; plushContainerWidth=100%25; plushNoTopMenu=0; hudson_auto_refresh=false

`

	req, err := readRequest(bufio.NewReader(strings.NewReader(testRequest)), false)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("%+v", req)
}
