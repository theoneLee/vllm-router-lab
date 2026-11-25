// Package kvcache holds a rank's logical prefix cache and the KV event feed
// that publishes its changes in vLLM's wire format.
package kvcache

import (
	"container/list"
	"crypto/sha256"

	"github.com/fxamacker/cbor/v2"
)

type Digest = [32]byte

func HashCBOR(v any) Digest {
	b, err := cbor.Marshal(v)
	if err != nil {
		panic(err)
	}
	return sha256.Sum256(b)
}
func blockKey(parent Digest, tokens []uint32) Digest { return HashCBOR([]any{parent[:], tokens, nil}) }

// DefaultRoot is vLLM's sha256_cbor root when no hash seed is set.
var DefaultRoot = HashCBOR("vllm-none-hash")

// Cache is an LRU of block identities. It is not safe for concurrent use.
type Cache struct {
	rootHash Digest
	blocks   map[Digest]*list.Element
	order    list.List
	capacity int
}

func New(capacity int, root Digest) *Cache { return &Cache{capacity: capacity, rootHash: root} }

// Prefix returns how many leading tokens are cached, in whole blocks.
func (c *Cache) Prefix(tokens []uint32, size int) int {
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

// Store caches every full block of tokens and returns the resulting events.
func (c *Cache) Store(tokens []uint32, blockSize int) []Event {
	if c.blocks == nil {
		c.blocks = make(map[Digest]*list.Element)
	}
	parent := c.rootHash
	var events []Event
	for start := 0; start+blockSize <= len(tokens); start += blockSize {
		chunk := tokens[start : start+blockSize]
		key := blockKey(parent, chunk)
		if el, ok := c.blocks[key]; ok {
			c.order.MoveToBack(el)
		} else {
			if c.order.Len() >= c.capacity {
				victim := c.order.Remove(c.order.Front()).(Digest)
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

// Clear empties the cache and returns the event announcing it.
func (c *Cache) Clear() []Event {
	c.blocks = nil
	c.order.Init()
	return []Event{allBlocksCleared{}}
}

// Len is the number of cached blocks.
func (c *Cache) Len() int { return c.order.Len() }
