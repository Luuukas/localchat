package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

type amsg struct {
	// resenttime

	// "localchat"

	// 该用户发送该信息的唯一连续序号
	seq      int64
	username string
	senttime time.Time
	text     string
}

func (m amsg) String() string {
	return fmt.Sprintf(`{"seq":"%d", "username":"%s", "senttime":"%s", "text":"%s"}`, m.seq,m.username, m.senttime, m.text)
}

// 双向链表节点，用于实现双向队列，实现单调队列
type dnode struct {
	// 延迟
	delay time.Duration
	// 邻近节点的用户名，用于映射到UDP连接
	pusername string
	pre *dnode
	nxt *dnode
}

// 双向链表
type deque struct {
	// 双向链表实现的队列的最大元素个数
	limitn int
	// 当前队列中的元素个数
	n int
	head *dnode
	rear *dnode
}

func newDeque() *deque {
	return &deque{
		limitn: 16,
		n: 0,
		head: nil,
		rear: nil,
	}
}

func (dq *deque)front() *dnode{
	return dq.head
}

func (dq *deque)back() *dnode{
	return dq.rear
}

func (dq *deque)pop_front() {
	if dq.n == 0 {
		return
	}
	dq.head = dq.head.nxt
	dq.n--
}

func (dq *deque)pop_back() {
	if dq.n == 0 {
		return
	}
	dq.rear = dq.rear.pre
	dq.n--
}

func (dq *deque) push_back(ndn *dnode) {
	if dq.n == 0 {
		dq.head = ndn
		dq.rear = ndn
		dq.n++
	}else {
		dq.rear.nxt = ndn
		dq.rear = ndn
		dq.n++
	}
}

func (dq *deque) inc_push(ndn *dnode) {
	for dq.n > 0 {
		if dq.back().delay > ndn.delay {
			dq.pop_back()
		} else {
			break
		}
	}
	dq.push_back(ndn)
	for dq.n > dq.limitn {
		dq.pop_front()
	}
}

func (dq *deque) use() (dn *dnode) {
	dn = dq.front()
	dq.pop_front()
	return
}

var (
	recvmsgs chan amsg

	//ip = net.ParseIP("224.0.0.250")
	ip = net.ParseIP("127.0.0.1")

	srcAddr = &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	dstAddr = &net.UDPAddr{IP: ip, Port: 9981}

	conn *net.UDPConn

	upgrader = websocket.Upgrader{}

	wsclosed chan struct{}
	
	wpassword = "Chong516"

	// 参与成员的username-公钥
	members = make(map[string] string)

	chatkey = "A symmetric key used to encrypt communications"

	musername = "luuukas"

	// host的公钥
	hpubkey = "hpubkey"
	// 自己的公钥
	mpubkey = "mpubkey"
	// 自己的私钥
	mprikey = "mprikey"

	// 对自己的每个发出去的msg进行标号
	mseqcnt int64 = 0

	// 便于向信息源再次获取丢失消息
	peerip = make(map[string] string)
	// 缓存信息，便于响应peers的再次获取请求，去除前8个byte，原前8个byte不用加密
	msgstore = make(map[string] map[int64] []byte)

	incqueue = newDeque()
)

// 向邻近节点或信息源重新获取某个用户发过的第seq条信息
func rerecvMsg(pusername string, seq int64) {
	info := make([]byte, 0)
	info = append(info, byte(len(pusername)))
	info = append(info, []byte(pusername)...)
	info = append(info, writeInt64(seq)...)

	dn := incqueue.use()
	answerer := pusername
	// 如果没有推荐的邻近peer，则直接向信息源重取
	if dn!=nil{
		answerer = dn.pusername
	}

	// 非组播，而是直接单播
	pip := peerip[answerer]
	dstaddr := &net.UDPAddr{IP: net.ParseIP(pip), Port: 9983}
	sendInfo(info, dstaddr)
}

// 收到别人想重新获取某条信息的请求，进行响应
func resendMsg(dstaddr *net.UDPAddr, data []byte, n int) {
	ulen:= data[0]
	username := string(data[1:1+ulen])
	seq := readInt64(data[1+ulen:1+ulen+8])

	newmsg := make([]byte, len(msgstore[username][seq]))

	newmsg = append(newmsg, writeInt64(time.Now().Unix())...)
	newmsg = append(newmsg, msgstore[username][seq]...)

	sendInfo(newmsg, dstaddr)
}

// 作为host的主机对入会请求进行验证
func verify(dstaddr *net.UDPAddr, data []byte, n int) {
	plen := data[0]
	// 验证会议密码
	ipassword := string(data[1:plen+1])
	if ipassword == wpassword {
		ulen := data[plen+1]
		iusername := string(data[plen+2:plen+2+ulen])
		// 该用户不在名单中
		if _, ok := members[iusername]; !ok {
			sendInfo([]byte("2"), dstaddr)
		} else {
			// 该用户在白名单中，保存其公钥
			members[iusername] = string(data[plen+2+ulen:n])

			// 发送前应先用对方的公钥进行加密

			// 发送通讯密钥
			sendInfo([]byte("0"+chatkey), dstaddr)
		}
	} else {
		sendInfo([]byte("1"), dstaddr)
	}
}

func init() {
	// 用于接收组播udp通讯的ip及端口
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
			n, dstaddr, err := listener.ReadFromUDP(data)
			if err != nil {
				fmt.Printf("error during read: %s", err)
			}
			m := parseMsg(data[:n])
			fmt.Println("debug: ",m)
			peerip[m.username] = dstaddr.IP.String()
			recvmsgs <- m
		}
	}()
	// 监听邻近节点重发的udp信息，监听本地9981
	rraddr, rrerr := net.ResolveUDPAddr("udp", "127.0.0.1:9982")
	if rrerr != nil {
		fmt.Println(rrerr)
	}
	rrlistener, rrerr := net.ListenUDP("udp",  rraddr)
	if rrerr != nil {
		fmt.Println(rrerr)
		return
	}
	fmt.Printf("Local: <%s> \n", rrlistener.LocalAddr().String())
	go func() {
		data := make([]byte, 1024)
		for {
			n, _, err := rrlistener.ReadFromUDP(data)
			if err != nil {
				fmt.Printf("error during read: %s", err)
			}
			recvmsgs <- parseMsg(data[:n])
		}
	}()
	// 监听邻近节点重发的请求，监听本地9982
	rqaddr, rqerr := net.ResolveUDPAddr("udp", "127.0.0.1:9983")
	if rqerr != nil {
		fmt.Println(rqerr)
	}
	rqlistener, rqerr := net.ListenUDP("udp",  rqaddr)
	if rqerr != nil {
		fmt.Println(rqerr)
		return
	}
	fmt.Printf("Local: <%s> \n", rqlistener.LocalAddr().String())
	go func() {
		data := make([]byte, 1024)
		for {
			n, dstaddr, err := rqlistener.ReadFromUDP(data)
			if err != nil {
				fmt.Printf("error during read: %s", err)
			}
			resendMsg(dstaddr, data, n)
		}
	}()
	// 用于发送组播的udp配置
	conn, err = net.DialUDP("udp", srcAddr, dstAddr)
	if err != nil {
		fmt.Println(err)
	}
}

func sendInfo(info []byte, dstaddr *net.UDPAddr){
	conn, err := net.DialUDP("udp", srcAddr, dstaddr)
	if err != nil {
		fmt.Println(err)
	}
	_, err = conn.Write(info)
	if err!=nil {
		fmt.Println(err)
	}
}

// 其中一个客户端成为Host，用于验证与分发通信密钥
func controllink() {
	// 监听入会请求/重发信息请求的组播ip及端口
	addr, err := net.ResolveUDPAddr("udp", "224.0.0.250:9984")
	if err != nil {
		fmt.Println(err)
	}
	listener, err := net.ListenMulticastUDP("udp", nil, addr)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("Host: <%s> \n", listener.LocalAddr().String())
	go func() {
		data := make([]byte, 1024)
		for {
			n, dstaddr, err := listener.ReadFromUDP(data)
			if err != nil {
				fmt.Printf("error during read: %s", err)
			}

			// 此处应先用host的私钥进行解密

			verify(dstaddr, data, n)

		}
	}()
}

// 请求参与会议
func join(password string) {
	dstaddr := &net.UDPAddr{IP: ip, Port: 9984}
	conn, err := net.DialUDP("udp", srcAddr, dstaddr)
	if err != nil {
		fmt.Println(err)
	}
	info := make([]byte, 0)
	info = append(info, byte(len(password)))
	info = append(info, []byte(password)...)
	info = append(info, byte(len(musername)))
	info = append(info, []byte(musername)...)
	info = append(info, []byte(mpubkey)...)
	conn.SetWriteDeadline(time.Now().Add(time.Duration(5*time.Second)))
	_, err = conn.Write(info)
	if err!=nil {
		fmt.Println(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Duration(5*time.Second)))
	resp := make([]byte, 1024)
	n, err := conn.Read(resp)
	if err!=nil {
		fmt.Println(err)
	}
	if resp[0] == byte('1') {
		fmt.Println("wrong password to enter the meeting...")
	} else if resp[0] == byte('2') {
		fmt.Println("you are not allowed to enter the meeting...")
	} else if resp[0] == byte('0') {
		// 应先用自己的密钥对 resp[1:n] 进行解密

		chatkey = string(resp[1:n])
	} else {
		fmt.Println("unknown code...")
	}
}

func boardcastMsg(msg []byte) {
	_, err := conn.Write(msg)
	if err!=nil {
		fmt.Println(err)
	}
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
	res = append(res, writeInt64(m.senttime.Unix())...)
	res = append(res, []byte("localchat")...)
	res = append(res, writeInt64(mseqcnt)...)
	mseqcnt++
	res = append(res, byte(len(m.username)&0xff))
	res = append(res, []byte(m.username)...)
	res = append(res, writeInt64(m.senttime.Unix())...)
	res = append(res, []byte(m.text)...)
	return
}

func parseMsg(res []byte) (m amsg) {
	// 前8个byte为明文
	fmt.Println(res)
	resenttime := time.Unix(readInt64(res[:8]), 0)

	res = res[8:]
	fmt.Println(res)
	// 用chatkey进行解密

	// 如果解密后首9个byte分别为localchat则认为该信息来自所参与的chat
	if string(res[:9]) != "localchat" {
		return amsg{}
	}
	m.seq = readInt64(res[9:9+8])
	unlen := int(res[9+8])
	m.username = string(res[8+9+1 : 8+9+unlen+1])

	if _, ok := msgstore[m.username]; !ok {
		msgstore[m.username] = make(map[int64] []byte)
	}
	msgstore[m.username][m.seq] = res

	m.senttime = time.Unix(readInt64(res[8+9+unlen+1:8+9+unlen+9]), 0)

	delay := time.Now().Sub(resenttime)
	if delay < maxDelay {
		// 与新信息发送时刻做差计算延迟，加入deque
		dn := &dnode{
			delay:     delay,
			pusername: m.username,
			pre:       nil,
			nxt:       nil,
		}
		incqueue.inc_push(dn)
	}

	m.text = string(res[8+9+unlen+9:])
	return
}

func IndexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// 根据请求body创建一个json解析器实例r
	decoder := json.NewDecoder(r.Body)

	// 用于存放参数key=value数据
	var params map[string]string

	// 解析参数 存入map
	decoder.Decode(&params)

	m := amsg{
		seq: mseqcnt,
		username: params["username"],
		senttime: time.Now(),
		text:     params["text"],
	}
	//r.ParseForm()
	//username := r.Form.Get("username")
	//text := r.Form.Get("text")
	//fmt.Printf("POST json: username=%s, text=%s\n", username, text)
	//m := amsg{
	//	username: username,
	//	senttime: time.Now(),
	//	text:     text,
	//}
	fmt.Println(m)
	boardcastMsg(intoUDP(m))
	fmt.Fprintf(w, `{"code":0}`)
}

func WsHandler(w http.ResponseWriter, r *http.Request) {
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	c, err := upgrader.Upgrade(w, r, nil)
	wsclosed = make(chan struct{})
	recvmsgs = make(chan amsg, 1024)
	defer c.Close()
	if err!=nil {
		fmt.Println(err)
	}

	go func() {
		_, _, err = c.ReadMessage()
		if err != nil {
			fmt.Println("websocket is closed.")
			close(wsclosed)
		}
	}()

	for {
		select {
		case m := <-recvmsgs:
			fmt.Println(m.String())
			c.WriteJSON(m.String())
		case <-wsclosed:
			return
		}
	}

}

func main() {
	defer conn.Close()
	http.HandleFunc("/parseform", IndexHandler)
	http.HandleFunc("/wsconn", WsHandler)
	http.ListenAndServe("127.0.0.1:8000", nil)
}

const (
	maxDelay = 500 * time.Millisecond
)