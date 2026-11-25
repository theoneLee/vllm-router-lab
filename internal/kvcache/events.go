package kvcache

import (
	"encoding/binary"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// Event is a cache change. The cache emits full digests. Only the encoder
// chooses the on-wire hash format.
type Event interface{ wire(intHashes bool) any }
type blockStored struct {
	key    Digest
	parent *Digest
	tokens []uint32
}
type blockRemoved struct{ key Digest }
type allBlocksCleared struct{}

type storedWire struct {
	Type            string   `msgpack:"type"`
	BlockHashes     []any    `msgpack:"block_hashes"`
	ParentBlockHash any      `msgpack:"parent_block_hash"`
	TokenIDs        []uint32 `msgpack:"token_ids"`
	BlockSize       int      `msgpack:"block_size"`
	LoRAID          *int     `msgpack:"lora_id"`
	LoRAName        *string  `msgpack:"lora_name"`
	Medium          string   `msgpack:"medium"`
	GroupIndex      int      `msgpack:"group_idx"`
	SpecKind        string   `msgpack:"kv_cache_spec_kind"`
}
type removedWire struct {
	Type        string `msgpack:"type"`
	BlockHashes []any  `msgpack:"block_hashes"`
	Medium      string `msgpack:"medium"`
	GroupIndex  int    `msgpack:"group_idx"`
}

func externalHash(key Digest, intHashes bool) any {
	if intHashes {
		return binary.BigEndian.Uint64(key[24:])
	}
	return key[:]
}
func (e blockStored) wire(intHashes bool) any {
	event := storedWire{
		Type:        "BlockStored",
		BlockHashes: []any{externalHash(e.key, intHashes)},
		TokenIDs:    e.tokens, BlockSize: len(e.tokens),
		Medium: "GPU", SpecKind: "full_attention",
	}
	if e.parent != nil {
		event.ParentBlockHash = externalHash(*e.parent, intHashes)
	}
	return event
}
func (e blockRemoved) wire(intHashes bool) any {
	return removedWire{Type: "BlockRemoved", BlockHashes: []any{externalHash(e.key, intHashes)}, Medium: "GPU"}
}
func (allBlocksCleared) wire(bool) any { return map[string]string{"type": "AllBlocksCleared"} }

func encodeBatch(events []Event, rank int, intHashes bool, ts time.Time) ([]byte, error) {
	encoded := make([]any, len(events))
	for i, event := range events {
		encoded[i] = event.wire(intHashes)
	}
	return msgpack.Marshal([]any{float64(ts.UnixMicro()) / 1e6, encoded, rank})
}
