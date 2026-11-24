package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestNativeChain(t *testing.T) {
	// Independent Python oracle checked into vllm-project/router at 159dcfc.
	seed := hashCBOR("0")
	tokens := []uint32{0, 1, 22, 23, 24, 25, 254, 255, 256, 257, 65534, 65535, 65536, 65537, 151644, 4294967295}
	hash := blockKey(seed, tokens)
	if hex.EncodeToString(hash[:]) != "abc28f6fcd2e01b1d065fc6d90ab913189c6eb5513c03faba6ff4b8dbea2901a" {
		t.Fatal("CBOR digest", fmt.Sprintf("%x", hash))
	}
	if fmt.Sprintf("%x", root) != "9bd96a485ad84efdafb72ee48a1d7a69bcead0f8f0433173941b276b9581eef0" {
		t.Fatal("default root")
	}
}
func TestEvict(t *testing.T) {
	c := cache{capacity: 2}
	a := []uint32{1, 2, 3, 4}
	_, e := c.access(a, 2)
	if len(e) != 2 || c.prefix(a, 2) != 4 {
		t.Fatal(e)
	}
	_, e = c.access([]uint32{8, 9}, 2)
	if e[0]["type"] != "BlockRemoved" || c.prefix(a, 2) != 0 {
		t.Fatal(e)
	}
}
func TestBatchCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBatcher(ctx, time.Millisecond, 2)
	dead, stop := context.WithCancel(ctx)
	stop()
	if b.step(dead, 100) || !b.step(ctx, 5) {
		t.Fatal("step or cancellation failed")
	}
}
