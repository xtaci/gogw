package aiohttp

// 此文件针对websocket的控制帧、大数据做进一步测试

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"log"
	"net/http"
	"strconv"
	"testing"
	"time"
)

var echoReqBody = []byte(`
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<script>  
window.addEventListener("load", function(evt) {

    var output = document.getElementById("output");
    var input = document.getElementById("input");
    var ws;

    var print = function(message) {
        var d = document.createElement("div");
        d.textContent = message;
        output.appendChild(d);
    };

    document.getElementById("open").onclick = function(evt) {
        if (ws) {
            return false;
        }
        ws = new WebSocket("ws://127.0.0.1:50006/echo");
        ws.onopen = function(evt) {
            print("OPEN");
        }
        ws.onclose = function(evt) {
            print("CLOSE");
            ws = null;
        }
        ws.onmessage = function(evt) {
            print("RESPONSE: " + evt.data);
        }
        ws.onerror = function(evt) {
            print("ERROR: " + evt.data);
        }
        return false;
    };

    document.getElementById("send").onclick = function(evt) {
        if (!ws) {
            return false;
        }
        print("SEND: " + input.value);
        ws.send(input.value);
        return false;
    };

    document.getElementById("close").onclick = function(evt) {
        if (!ws) {
            return false;
        }
        ws.close();
        return false;
    };

});
</script>
</head>
<body>
<table>
<tr><td valign="top" width="50%">
<p>Click "Open" to create a connection to the server, 
"Send" to send a message to the server and "Close" to close the connection. 
You can change the message and send multiple times.
<p>
<form>
<button id="open">Open</button>
<button id="close">Close</button>
<p><input id="input" type="text" value="Hello world!">
<button id="send">Send</button>
</form>
</td><td valign="top" width="50%">
<div id="output"></div>
</td></tr></table>
</body>
</html>
`)

var (
	errPath = errors.New("incorrect path")
)

func home(ctx *ClientContext) error {
	if 0 == bytes.Compare(ctx.Header.requestURI, []byte("/")) || 0 == bytes.Compare(ctx.Header.requestURI, []byte("/index.html")) {
		ctx.Response.SetStatusCode(StatusOK)
		//ctx.ResponseData = []byte("Http home page")
		ctx.ResponseData = echoReqBody
	} else {
		ctx.Response.SetStatusCode(StatusBadRequest)
	}

	return nil
}

func echo(ctx *ClientContext) error {
	wsMSG := &ctx.WSMsg
	wsMSG.RspData = append(wsMSG.RspData, wsMSG.ReqData...)
	wsMSG.MessageType = TextMessage

	// 测试服务端主动发送
	rspdata := string(wsMSG.RspData)
	go func() {
		i := 0
		var msg WSMessage
		for {
			i++
			time.Sleep(time.Second)
			msg.RspData = append(msg.RspData, []byte(strconv.Itoa(i)+" push test "+rspdata)...)
			msg.MessageType = TextMessage
			msg.RspHeader = make([]byte, 0, maxFrameHeaderSize)
			ctx.proc.writeWSRspData(ctx, ctx.conn, &msg)
			if i == 12 {
				msg.RspData = append(msg.RspData, []byte(strconv.Itoa(i)+" push stop "+rspdata)...)
				msg.MessageType = TextMessage
				msg.RspHeader = make([]byte, 0, maxFrameHeaderSize)
				ctx.proc.writeWSRspData(ctx, ctx.conn, &msg)
				break
			}
		}
	}()

	return nil
}

func TestWS(t *testing.T) {

	HandleWSFunc("/echo", echo)
	const numServer = 1
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	for i := 0; i < numServer; i++ {
		server, err := NewServer(":50006", 256*1024*1024, home, nil)
		if err != nil {
			panic(err)
		}
		server.SetLoopAffinity(i * 2)
		server.SetPoolerAffinity(i * 2)
		go server.ListenAndServe()
	}

	select {}
}

var actualMessage string

func wsponghandler(appData string) error {
	actualMessage = appData
	fmt.Println(actualMessage)
	return nil
}

func TestPing(t *testing.T) {
	c, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:50006/echo", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer c.Close()

	const message = "this is a ping/pong messsage"
	err = c.WriteControl(PingMessage, []byte(message), time.Now().Add(time.Second))
	if err != nil {
		fmt.Println(err)
		return
	}

	c.SetPongHandler(wsponghandler)
	iType, _, err := c.NextReader() // NextReader设置了只会返回bin txt消息 所以会卡这里 通过 wsponghandler 的打印来看是否返回
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(iType)
}

func TestFraming(t *testing.T) {
	c, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:50006/echo", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer c.Close()

	err = c.WriteMessage(TextMessage, []byte("hello world"))
	if err != nil {
		fmt.Println(err)
		return
	}

	iType, msgRsp, err := c.ReadMessage()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(iType)
	fmt.Println(string(msgRsp))

	frameSizes := []int{
		1, 2, 124, 125, 126, 127, 128, 129, 65534, 65535, 65536, 65537, 65538, 88888,
	}

	writeBuf := make([]byte, 102400)
	for i := range writeBuf {
		writeBuf[i] = byte(i)
	}

	for _, n := range frameSizes {
		fmt.Println("start sending msg ", n)
		err = c.WriteMessage(TextMessage, writeBuf[:n])
		if err != nil {
			fmt.Println(err)
			break
		}

		iType, msgRsp, err := c.ReadMessage()
		if err != nil {
			fmt.Println(err)
			break
		}

		fmt.Println("Rec msg ", n, iType, len(msgRsp))
	}

}
