package main

import (
	"Rtp_Http_Flv/container/rtp"
	"Rtp_Http_Flv/utils"
	"errors"
	"fmt"
	"github.com/emirpasic/gods/lists/arraylist"
	"sync"
)

//type rtpQueueItem struct {
//	packet *RTPPacket
//	seq    uint16
//}

//[1,2,3,0,0]

type queue struct {
	m  sync.RWMutex
	wg sync.WaitGroup
	//maxSize      int
	Ssrc              uint32          //队列所属的流
	FirstSeq          uint16          //第一个Rtp包的序号
	PaddingWindowSize int             //滑动窗口大小
	queue             *arraylist.List //rtpPacket队列
	checked           bool            //窗口内是否都已检验
	outChan           chan interface{}
	inChan            chan interface{}
	init              bool
	flvRecord         *FlvRecord      //解析flv结构
	flvWriters        *arraylist.List //http-flv对象
	flvFile           *utils.File     //录制文件
	cache             *SegmentCache
	accPackets        int //记录收到包的数量
}

func newQueue(ssrc uint32, wz int, record *FlvRecord, flvFile *utils.File) *queue {
	return &queue{
		queue:             arraylist.New(),
		PaddingWindowSize: wz,
		Ssrc:              ssrc,
		flvRecord:         record,
		flvFile:           flvFile,
		outChan:           make(chan interface{}, 10),
		inChan:            make(chan interface{}, 10),
		flvWriters:        arraylist.New(),
		cache:             NewCache(),
	}
}

func (q *queue) RecvPacket() error {
	for {
		p, ok := <-q.inChan
		if ok {
			q.Enqueue(p.(*rtp.RtpPack))
			if q.accPackets == q.PaddingWindowSize {
				q.Check()
			}
			for q.queue.Size() > q.PaddingWindowSize {
				protoRp := q.Dequeue()
				q.outChan <- protoRp
			}
		} else {
			return errors.New("closed")
		}
	}
}

//	func (q *queue) Play() {
//		for {
//			protoRp := q.Dequeue()
//			err := extractFlv(protoRp, q)
//			if err != nil {
//				q.flvRecord.Reset()
//			}
//			if q.queue.Size() <= q.PaddingWindowSize {
//				break
//			}
//		}
//	}
func (q *queue) Play() error {
	for {
		protoRp, ok := <-q.outChan
		if ok {
			err := extractFlv(protoRp, q)
			if err != nil {
				q.flvRecord.Reset()
			}
		} else {
			return errors.New("closed")
		}
	}
}

func (q *queue) Enqueue(rp *rtp.RtpPack) {
	q.m.Lock()
	defer q.m.Unlock()

	q.accPackets += 1

	seq := rp.SequenceNumber
	if q.queue.Size() == 0 { //队列中还没有元素
		q.FirstSeq = seq
		q.queue.Add(rp)
	} else {
		var relative int
		if q.FirstSeq > seq {
			if int(q.FirstSeq-seq) > 60000 { //序列号到头
				relative = 65536 - int(q.FirstSeq) + int(seq)
			} else { //过时的包
				fmt.Println("过时的包 ", seq, " ", q.FirstSeq)
				return
			}
		} else {
			relative = int(seq - q.FirstSeq)
		}
		if relative <= q.queue.Size() { //没到队列终点
			q.queue.Set(relative, rp)
		} else {
			for i := q.queue.Size(); i <= relative; i++ {
				if i != relative {
					q.queue.Set(i, nil)
					continue
				}
				q.queue.Set(i, rp)
			}
		}

	}

}

func (q *queue) offerPacket() { //channel方式，多协程读取队列中的包，已弃用
	//q.reading = true
	for {
		rp_end, _ := q.queue.Get(q.PaddingWindowSize - 1)
		if rp_end == nil {
			continue
		}
		rp0, _ := q.queue.Get(0)
		if q.queue.Size() > q.PaddingWindowSize {

			//fmt.Println(rp0)
			q.outChan <- rp0

			q.m.Lock()
			rp, _ := q.queue.Get(q.PaddingWindowSize)
			if rp == nil {
				//重传
				seq := q.FirstSeq + uint16(q.PaddingWindowSize)
				fmt.Println("序号为", seq, "的包丢失，进行quic重传")
				go GetByQuic(q, seq)
				//q.queue.Set(i, pkt)
			}
			q.queue.Remove(0)
			q.FirstSeq += 1
			q.m.Unlock()
		}
	}
}

func (q *queue) Dequeue() interface{} { //必须确保paddingsize内的rtp包已到达
	//确保窗口内的包都存在
	rp, _ := q.queue.Get(q.PaddingWindowSize)
	if rp == nil {
		//重传
		seq := q.FirstSeq + uint16(q.PaddingWindowSize)
		fmt.Println("packet lost seq = ", seq, ", ssrc = ", q.Ssrc, "run quic request")
		GetByQuic(q, seq)
		//q.queue.Set(i, pkt)
	}

	var res interface{}
	res, _ = q.queue.Get(0)
	q.m.Lock()
	q.queue.Remove(0)
	q.FirstSeq += 1
	q.m.Unlock()
	return res
}

func (q *queue) Check() int { //检查窗口内队列Rtp的存在性和有序性

	re_trans := 0
	//rtpParser := parser.NewRtpParser()
	for i := 0; i < q.PaddingWindowSize; i++ {
		rp, _ := q.queue.Get(i)
		if rp == nil {
			//pkt := rtpParser.Parse([]byte{byte(128), byte(137), byte(16), byte(80), byte(14), byte(182),
			//	byte(27), byte(244), byte(0), byte(15), byte(145), byte(144), byte(8), byte(0), byte(1)}) //quic重传
			//q.queue.Set(i, pkt)
			fmt.Println("packet lost seq = ", int(q.FirstSeq)+i, ", ssrc = ", q.Ssrc, "run quic request")
			GetByQuic(q, q.FirstSeq+uint16(i))
			re_trans += 1
		}
		//if rp.(*rtp.RtpPack).SequenceNumber != q.FirstSeq+uint16(i) {
		//	fmt.Println("err ！Rtp Queue not sorted, FirstSeq:", q.FirstSeq, ", i:", i, ",SeqNum:", rp.(*rtp.RtpPack).SequenceNumber)
		//}
	}
	if re_trans == 0 {
		q.checked = true
	}
	return re_trans
}
func (q *queue) print() {
	fmt.Println("rtp队列长度：", q.queue.Size())
	fmt.Print("rtp队列：")
	for i := 0; i < q.queue.Size(); i++ {
		rp, _ := q.queue.Get(i)
		if rp == nil {
			fmt.Print(" nil")
		} else {
			fmt.Print(rp.(*rtp.RtpPack).SequenceNumber, " ")
		}
	}
	fmt.Println()

}

func (q *queue) Close() {
	if q.flvFile != nil {
		q.flvFile.Close()
	}
}
