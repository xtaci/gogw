package gogw

import (
	"bufio"
	"bytes"
	"net/textproto"
	"testing"
)

func TestBufferdIO(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte("hello 1234134 "))
	tp := textproto.NewReader(bufio.NewReader(&buf))
	str, err := tp.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(str)
}
