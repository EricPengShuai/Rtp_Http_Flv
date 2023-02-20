package main

import (
	"fmt"
	"go-mpu/container/rtp"
	"go-mpu/parser"
	"net"
)

//var wg sync.WaitGroup

type FlvRecord struct {
	HttpServer *Server
	flv_tag    []byte
	pos        int
	last_ts    uint32
}

func main() {

	receiveRtp()

}

func receiveRtp() {
	address := "239.0.0.0:5222"

	addr, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		panic(err)
	}

	// Open up a connection
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	//conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		panic(err)
	}

	flvFile, err := CreateFile("./recv.flv")
	if err != nil {
		fmt.Println("Create FLV dump file error:", err)
		return
	}
	defer func() {
		if flvFile != nil {
			flvFile.Close()
		}
	}()

	rtpQueue := newQueue(10)
	flvRecord := &FlvRecord{
		startHTTPFlv(), nil, 0, uint32(0),
	}

	for {
		//读udp数据
		buff := make([]byte, 2*1024)
		//num, err := conn.Read(buff)
		num, _, err := conn.ReadFromUDP(buff)
		if err != nil {
			continue
		}

		//解析为rtp包
		data := buff[:num]
		rtpParser := parser.NewRtpParser()
		rp := rtpParser.Parse(data)
		if rp == nil {
			continue
		}

		//Rtp包顺序存放到队列中
		rtpQueue.Enqueue(rp)

		if rtpQueue.queue.Size() < 2*rtpQueue.PaddingWindowSize { //刚开始先缓存一定量
			continue
		} else if !rtpQueue.checked {
			fmt.Println("rtp队列进行check")
			rtpQueue.Check()
			continue
		}
		//到达一定量后就从队列中取rtp了
		rp = rtpQueue.Dequeue() //阻塞
		err = extractFlv(rp, flvRecord, rtpQueue, flvFile, false)
		if err != nil {
			panic(err)
		}

	}

}

//从rtp包中提取出flv_tag，根据record信息组合分片，debug打印调试信息
func extractFlv(rp *rtp.RtpPack, record *FlvRecord, rtpQueue *queue, flvFile *File, debug bool) error {
	payload := rp.Payload
	marker := rp.Marker
	new_ts := rp.Timestamp

	tmpBuf := make([]byte, 4)
	if debug {
		fmt.Println("-----------------", rp.SequenceNumber, "-----------------")
	}
	if int(rp.SequenceNumber)%100 == 0 {
		rtpQueue.print()
	}

	if marker == byte(0) { //该帧未结束
		if new_ts > record.last_ts { //该帧是初始帧
			// Read tag size
			copy(tmpBuf[1:], payload[1:4])
			TagSize := uint32(tmpBuf[1])<<16 | uint32(tmpBuf[2])<<8 | uint32(tmpBuf[3]) + uint32(11)
			//fmt.Println("新建初始帧长度为", TagSize)
			record.flv_tag = make([]byte, TagSize)

			copy(record.flv_tag[record.pos:record.pos+len(payload)], payload)
			record.pos += len(payload)
		} else { //该帧是中间帧
			copy(record.flv_tag[record.pos:record.pos+len(payload)], payload)
			record.pos += len(payload)
		}
	} else { //该帧是结束帧
		if new_ts > record.last_ts { //没有之前分片
			record.flv_tag = payload
		} else { //有前面的分片
			//fmt.Println("pos===", pos)
			//fmt.Println(len(payload))
			copy(record.flv_tag[record.pos:record.pos+len(payload)], payload)
		}
		//得到一个flv tag

		//有客户端就将flv数据发给客户端
		if record.HttpServer.flvWriter != nil {
			//FlvTagList.PushBack(flv_tag)
			err := record.HttpServer.flvWriter.Write(record.flv_tag)
			if err != nil {
				return err
			}
		}
		//录制到文件中
		err := flvFile.WriteTagDirect(record.flv_tag)
		if err != nil {
			return err
		}
		//fmt.Println("rtp seq:", rp.SequenceNumber, ",payload size: ", len(flv_tag), ",rtp timestamp: ", rp.Timestamp)

		record.flv_tag = nil
		record.pos = 0

	}
	record.last_ts = new_ts
	return nil
}
