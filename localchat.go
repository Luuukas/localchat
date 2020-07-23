package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

type amsg struct {
	username string
	senttime time.Time
	text     string
}

func (m amsg) String() string {
	return fmt.Sprintf(`{"username":"%s", "senttime":"%s", "text":"%s"}`, m.username, m.senttime, m.text)
}

var (
	recvmsgs = make(chan amsg, 1024)

	//ip = net.ParseIP("224.0.0.250")
	ip = net.ParseIP("127.0.0.1")

	srcAddr = &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	dstAddr = &net.UDPAddr{IP: ip, Port: 9981}

	conn *net.UDPConn

	upgrader = websocket.Upgrader{}
)

func init() {
	addr, err := net.ResolveUDPAddr("udp", "224.0.0.250:9981")
	if err != nil {
		fmt.Println(err)
	}
	listener, err := net.ListenMulticastUDP("udp", nil, addr)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("Local: <%s> \n", listener.LocalAddr().String())
	go func() {
		data := make([]byte, 1024)
		for {
			n, _, err := listener.ReadFromUDP(data)
			if err != nil {
				fmt.Printf("error during read: %s", err)
			}
			recvmsgs <- parseMsg(data[:n])
		}
	}()

	conn, err = net.DialUDP("udp", srcAddr, dstAddr)
	if err != nil {
		fmt.Println(err)
	}
}

func sendMsg(msg []byte) {
	conn.Write(msg)
}

func writeInt64(n int64) (buf []byte) {
	buf = make([]byte, 8)
	// LittleEndian
	for i := 0; i < 8; i++ {
		buf[i] = byte(n & 0xff)
		n = n >> 8
	}
	return
}

func readInt64(buf []byte) (n int64) {
	n = 0
	for i := 7; i >= 0; i-- {
		n = n << 8
		n = n | int64(buf[i])
	}
	return
}

func intoUDP(m amsg) (res []byte) {
	res = make([]byte, 0)
	res = append(res, byte(len(m.username)&0xff))
	res = append(res, []byte(m.username)...)
	res = append(res, writeInt64(m.senttime.Unix())...)
	res = append(res, []byte(m.text)...)
	return
}

func parseMsg(res []byte) (m amsg) {
	unlen := int(res[0])
	m.username = string(res[1 : unlen+1])
	m.senttime = time.Unix(readInt64(res[unlen+1:unlen+9]), 0)
	m.text = string(res[unlen+9:])
	return
}

func IndexHandler(w http.ResponseWriter, r *http.Request) {
	//// 根据请求body创建一个json解析器实例r
	//decoder := json.NewDecoder(r.Body)
	//
	//// 用于存放参数key=value数据
	//var params map[string]string
	//
	//// 解析参数 存入map
	//decoder.Decode(&params)
	//
	//fmt.Printf("POST json: username=%s, text=%s\n", params["username"], params["text"])
	//m := amsg{
	//	username: params["username"],
	//	senttime: time.Now(),
	//	text:     params["text"],
	//}
	r.ParseForm()
	username := r.Form.Get("username")
	text := r.Form.Get("text")
	fmt.Printf("POST json: username=%s, text=%s\n", username, text)
	m := amsg{
		username: username,
		senttime: time.Now(),
		text:     text,
	}
	fmt.Println(m)
	sendMsg(intoUDP(m))
	fmt.Fprintf(w, `{"code":0}`)
}

func WsHandler(w http.ResponseWriter, r *http.Request) {
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	c, err := upgrader.Upgrade(w, r, nil)
	defer c.Close()
	if err!=nil {
		fmt.Println(err)
	}
	for {
		m := <-recvmsgs
		fmt.Println(m.String())
		c.WriteJSON(m.String())
	}

}

func main() {
	defer conn.Close()
	http.HandleFunc("/parseform", IndexHandler)
	http.HandleFunc("/wsconn", WsHandler)
	http.ListenAndServe("127.0.0.1:8000", nil)
}
