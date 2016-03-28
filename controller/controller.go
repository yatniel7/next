package controller

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/chzyer/flow"
	"github.com/chzyer/next/packet"
)

type Controller struct {
	flow   *flow.Flow
	in     chan *Request
	out    chan *packet.Packet
	toDC   chan<- *packet.Packet
	fromDC <-chan *packet.Packet
	reqId  uint32

	staging      map[uint32]*Request
	stagingGruad sync.Mutex
}

func NewController(f *flow.Flow, toDC chan<- *packet.Packet, fromDC <-chan *packet.Packet) *Controller {
	ctl := &Controller{
		in:      make(chan *Request, 8),
		out:     make(chan *packet.Packet),
		toDC:    toDC,
		fromDC:  fromDC,
		staging: make(map[uint32]*Request),
	}
	f.ForkTo(&ctl.flow, ctl.Close)
	go ctl.readLoop()
	go ctl.writeLoop()
	go ctl.resendLoop()
	return ctl
}

func (c *Controller) GetOutChan() <-chan *packet.Packet {
	return c.out
}

func (c *Controller) GetReqId() uint32 {
	return atomic.AddUint32(&c.reqId, 1)
}

func (c *Controller) Close() {
	c.flow.Close()
}

func (c *Controller) WriteChan() chan *Request {
	return c.in
}

type Request struct {
	Packet *packet.Packet
	Reply  chan *packet.Packet
}

func NewRequest(p *packet.Packet, reply bool) *Request {
	req := &Request{Packet: p}
	if reply {
		req.Reply = make(chan *packet.Packet)
	}
	return req
}

func (c *Controller) send(req *Request) *packet.Packet {
	select {
	case c.in <- req:
		if req.Reply != nil {
			select {
			case rep := <-req.Reply:
				return rep
			case <-c.flow.IsClose():
			}
		}
	case <-c.flow.IsClose():
	}
	return nil
}

func (c *Controller) Request(req *packet.Packet) *packet.Packet {
	return c.send(&Request{
		Packet: req,
		Reply:  make(chan *packet.Packet),
	})
}

func (c *Controller) Send(req *packet.Packet) {
	c.send(&Request{Packet: req})
}

func (c *Controller) readLoop() {
	c.flow.Add(1)
	defer c.flow.DoneAndClose()
loop:
	for {
		select {
		case <-c.flow.IsClose():
			break loop
		case p := <-c.fromDC:
			if p.Type.IsResp() {
				// println("I got Reply:", p.IV.ReqId)
				c.stagingGruad.Lock()
				if staging := c.staging[p.IV.ReqId]; staging != nil {
					if staging.Reply != nil {
						select {
						case staging.Reply <- p:
						default:
						}
					}
					delete(c.staging, p.IV.ReqId)
				}
				c.stagingGruad.Unlock()
			} else {
				// println("I need Reply to:", p.IV.ReqId)
				select {
				case c.out <- p:
				case <-c.flow.IsClose():
					break loop
				}
			}
		}
	}
}

func (c *Controller) resendLoop() {
	for _ = range time.Tick(time.Second) {
		// println(len(c.staging))
	}
}

func (c *Controller) writeLoop() {
	c.flow.Add(1)
	defer c.flow.DoneAndClose()

loop:
	for {
		select {
		case <-c.flow.IsClose():
			break loop
		case req := <-c.in:
			// add to staging
			c.stagingGruad.Lock()
			if req.Packet.Type.IsReq() {
				req.Packet.InitIV(c)
				c.staging[req.Packet.IV.ReqId] = req
				// println("I add to stage: ",
				//	req.Packet.IV.ReqId, req.Packet.Type.String())
			} else {
				// println("I reply to:", req.Packet.IV.ReqId)
			}
			c.toDC <- req.Packet
			c.stagingGruad.Unlock()
		}
	}
}