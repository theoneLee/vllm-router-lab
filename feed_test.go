package main

import (
	"bytes"
	"encoding/binary"
	"testing"

	zmq "github.com/go-zeromq/zmq4"
)

// Capture live sends at the socket boundary. The real replay socket and its
// sentinel are exercised separately by scripts/matrix.py.
type recordingPublisher struct {
	zmq.Socket
	live []zmq.Msg
}

func (p *recordingPublisher) SendMulti(m zmq.Msg) error {
	p.live = append(p.live, m)
	return nil
}

func TestLiveDropAndReplayWindow(t *testing.T) {
	pub := &recordingPublisher{}
	f := feed{pub: pub, topic: "rank1", rank: 1, window: 2}
	events := []cacheEvent{allBlocksCleared{}}
	f.emit(events)
	f.drop = true
	f.emit(events)
	dropped := f.replayFrom(1)
	if len(dropped) != 1 || dropped[0].seq != 1 || len(dropped[0].body) == 0 {
		t.Fatalf("dropped batch missing in replay: %v", dropped)
	}
	f.emit(events)
	if len(pub.live) != 2 {
		t.Fatalf("got %d live batches, want 2", len(pub.live))
	}
	for i, seq := range []uint64{0, 2} {
		frames := pub.live[i].Frames
		if len(frames) != 3 || string(frames[0]) != f.topic || len(frames[1]) != 8 || binary.BigEndian.Uint64(frames[1]) != seq {
			t.Fatalf("unexpected live framing: %v", frames)
		}
	}
	kept := f.replayFrom(0)
	if len(kept) != 2 || kept[0].seq != 1 || kept[1].seq != 2 || !bytes.Equal(kept[0].body, dropped[0].body) || !bytes.Equal(kept[1].body, pub.live[1].Frames[2]) {
		t.Fatalf("unexpected replay after eviction: %v", kept)
	}
	if last := f.replayFrom(2); len(last) != 1 || last[0].seq != 2 {
		t.Fatalf("replay must include the requested sequence: %v", last)
	}
	if len(f.replayFrom(3)) != 0 {
		t.Fatal("future sequence returned history")
	}
}
