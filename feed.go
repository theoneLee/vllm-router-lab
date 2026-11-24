package main

import (
	"context"
	"encoding/binary"
	"log"
	"sync"
	"time"

	zmq "github.com/go-zeromq/zmq4"
)

func number(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

type frame struct {
	seq  uint64
	body []byte
}
type feed struct {
	pub, replay  zmq.Socket
	topic        string
	rank, window int
	intHashes    bool
	mu           sync.Mutex
	seq, replays uint64
	history      []frame
	drop         bool
}

func newFeed(ctx context.Context, topic string, rank, window int, intHashes bool) (*feed, error) {
	f := &feed{
		pub: zmq.NewPub(ctx), replay: zmq.NewRouter(ctx),
		rank: rank, topic: topic, window: window, intHashes: intHashes,
	}
	for _, s := range []zmq.Socket{f.pub, f.replay} {
		if err := s.Listen("tcp://127.0.0.1:0"); err != nil {
			return nil, err
		}
	}
	go f.serveReplay()
	return f, nil
}
func (f *feed) serveReplay() {
	for {
		m, e := f.replay.Recv()
		if e != nil {
			return
		}
		if len(m.Frames) != 3 || len(m.Frames[1]) != 0 || len(m.Frames[2]) != 8 {
			continue
		}
		from := binary.BigEndian.Uint64(m.Frames[2])
		items := f.replayFrom(from)
		send := func(topic string, seq uint64, b []byte) error {
			return f.replay.SendMulti(zmq.NewMsgFrom(m.Frames[0], nil, []byte(topic), number(seq), b))
		}
		for _, x := range items {
			if e = send(f.topic, x.seq, x.body); e != nil {
				break
			}
		}
		if e == nil {
			_ = send("", ^uint64(0), nil)
		}
	}
}

// Snapshot under the lock so sending a replay cannot stall live publication.
func (f *feed) replayFrom(from uint64) []frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replays++
	for i, batch := range f.history {
		if batch.seq >= from {
			return append([]frame(nil), f.history[i:]...)
		}
	}
	return nil
}

func (f *feed) emit(events []cacheEvent) {
	if f == nil || len(events) == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	body, e := encodeBatch(events, f.rank, f.intHashes, time.Now())
	if e != nil {
		panic(e)
	}
	entry := frame{f.seq, body}
	f.seq++
	f.history = append(f.history, entry)
	if len(f.history) > f.window {
		f.history = f.history[1:]
	}
	if f.drop {
		f.drop = false
		return
	}
	if e = f.pub.SendMulti(zmq.NewMsgFrom([]byte(f.topic), number(entry.seq), body)); e != nil {
		log.Printf("publish: %v", e)
	}
}
func (f *feed) metadata() object {
	return object{"endpoint": "tcp://" + f.pub.Addr().String(), "replay_endpoint": "tcp://" + f.replay.Addr().String(), "topic": f.topic, "publisher": "zmq", "encoding": "msgpack", "schema_version": 1, "enable_kv_cache_events": true}
}
