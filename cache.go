package main

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	zmq "github.com/go-zeromq/zmq4"
	"github.com/vmihailenco/msgpack/v5"
)

type object = map[string]any
type digest = [32]byte

func hashCBOR(v any) digest {
	b, err := cbor.Marshal(v)
	if err != nil {
		panic(err)
	}
	return sha256.Sum256(b)
}
func blockKey(parent digest, tokens []uint32) digest { return hashCBOR([]any{parent[:], tokens, nil}) }

var root = hashCBOR("vllm-none-hash")

func number(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

type frame struct {
	seq  uint64
	body []byte
}
type feed struct {
	pub, replay  zmq.Socket
	topic        string
	rank, window int
	mu           sync.Mutex
	seq, replays uint64
	history      []frame
	drop         bool
}

func newFeed(ctx context.Context, topic string, rank, window int) (*feed, error) {
	f := &feed{pub: zmq.NewPub(ctx), replay: zmq.NewRouter(ctx), rank: rank, topic: topic, window: window}
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
		f.mu.Lock()
		items := append([]frame(nil), f.history...)
		f.replays++
		f.mu.Unlock()
		send := func(topic string, seq uint64, b []byte) error {
			return f.replay.SendMulti(zmq.NewMsgFrom(m.Frames[0], nil, []byte(topic), number(seq), b))
		}
		for _, x := range items {
			if x.seq >= from {
				if e = send(f.topic, x.seq, x.body); e != nil {
					break
				}
			}
		}
		if e == nil {
			_ = send("", ^uint64(0), nil)
		}
	}
}
func (f *feed) emit(events []object) {
	if f == nil || len(events) == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	body, e := msgpack.Marshal([]any{float64(time.Now().UnixMicro()) / 1e6, events, f.rank})
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

type cache struct {
	rootHash  digest
	intHashes bool
	blocks    map[digest]*list.Element
	order     list.List
	capacity  int
}

// vLLM optionally exports the low 64 bits of the digest for legacy subscribers.
func (c *cache) external(key digest) any {
	if c.intHashes {
		return binary.BigEndian.Uint64(key[24:])
	}
	return key[:]
}

func (c *cache) prefix(tokens []uint32, size int) int {
	parent := c.rootHash
	for i := 0; i+size <= len(tokens); i += size {
		key := blockKey(parent, tokens[i:i+size])
		el := c.blocks[key]
		if el == nil {
			return i
		}
		c.order.MoveToBack(el)
		parent = key
	}
	return len(tokens) / size * size
}
func (c *cache) access(tokens []uint32, blockSize int) (int, []object) {
	if c.blocks == nil {
		c.blocks = make(map[digest]*list.Element)
	}
	parent := c.rootHash
	hits := 0
	prefix := true
	var events []object
	for start := 0; start+blockSize <= len(tokens); start += blockSize {
		chunk := tokens[start : start+blockSize]
		key := blockKey(parent, chunk)
		if el, ok := c.blocks[key]; ok {
			c.order.MoveToBack(el)
			if prefix {
				hits += blockSize
			}
		} else {
			prefix = false
			if c.order.Len() >= c.capacity {
				victim := c.order.Remove(c.order.Front()).(digest)
				delete(c.blocks, victim)
				events = append(events, object{"type": "BlockRemoved", "block_hashes": []any{c.external(victim)}, "medium": "GPU", "group_idx": 0})
			}
			var p any
			if start > 0 {
				p = c.external(parent)
			}
			events = append(events, object{"type": "BlockStored", "block_hashes": []any{c.external(key)}, "parent_block_hash": p, "token_ids": chunk, "block_size": blockSize, "lora_id": nil, "lora_name": nil, "medium": "GPU", "group_idx": 0, "kv_cache_spec_kind": "full_attention"})
			c.blocks[key] = c.order.PushBack(key)
		}
		parent = key
	}
	return hits, events
}
func (c *cache) clear()     { c.blocks = nil; c.order.Init() }
func endpoint(i int) string { return fmt.Sprintf("http://127.0.0.1:%d", i) }
