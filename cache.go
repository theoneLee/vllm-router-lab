package main

import (
	"container/list"
	"crypto/sha256"

	"github.com/fxamacker/cbor/v2"
)

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

type cache struct {
	rootHash digest
	blocks   map[digest]*list.Element
	order    list.List
	capacity int
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
func (c *cache) store(tokens []uint32, blockSize int) []cacheEvent {
	if c.blocks == nil {
		c.blocks = make(map[digest]*list.Element)
	}
	parent := c.rootHash
	var events []cacheEvent
	for start := 0; start+blockSize <= len(tokens); start += blockSize {
		chunk := tokens[start : start+blockSize]
		key := blockKey(parent, chunk)
		if el, ok := c.blocks[key]; ok {
			c.order.MoveToBack(el)
		} else {
			if c.order.Len() >= c.capacity {
				victim := c.order.Remove(c.order.Front()).(digest)
				delete(c.blocks, victim)
				events = append(events, blockRemoved{key: victim})
			}
			stored := blockStored{key: key, tokens: chunk}
			if start > 0 {
				previous := parent
				stored.parent = &previous
			}
			events = append(events, stored)
			c.blocks[key] = c.order.PushBack(key)
		}
		parent = key
	}
	return events
}
func (c *cache) clear() { c.blocks = nil; c.order.Init() }
