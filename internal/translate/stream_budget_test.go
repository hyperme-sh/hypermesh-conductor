package translate

import (
	"strings"
	"testing"
)

func TestStreamsShareRetainedOutputBudget(t *testing.T) {
	old := maxRetainedStreamBytes
	maxRetainedStreamBytes = 4096
	t.Cleanup(func() { maxRetainedStreamBytes = old })
	streams := []*Stream{}
	exhausted := false
	for i := 0; i < 128; i++ {
		s := NewStream(ProtoResponses, ProtoMessages)
		streams = append(streams, s)
		s.Consume([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" + strings.Repeat("x", 512) + "\"}}\n\n"))
		if s.TooLarge() {
			exhausted = true
			break
		}
	}
	if !exhausted {
		t.Error("independent streams bypassed shared budget")
	}
	if used := retainedStreamBytes.Load(); used > 4096 {
		t.Errorf("retained %d", used)
	}
	for _, s := range streams {
		s.Close()
		s.Close()
	}
	if used := retainedStreamBytes.Load(); used != 0 {
		t.Errorf("leaked %d bytes", used)
	}
}

func TestMessageIDsAndPartialLinesConsumeSharedBudget(t *testing.T) {
	old := maxRetainedStreamBytes
	maxRetainedStreamBytes = 1024
	t.Cleanup(func() { maxRetainedStreamBytes = old })
	first := NewStream(ProtoChat, ProtoMessages)
	defer first.Close()
	first.Consume([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"" + strings.Repeat("i", 700) + "\"}}\n\n"))
	if first.TooLarge() {
		t.Fatal("first ID should fit")
	}
	second := NewStream(ProtoChat, ProtoMessages)
	defer second.Close()
	second.Consume([]byte(strings.Repeat("p", 400)))
	if !second.TooLarge() {
		t.Error("partial event bypassed retained ID reservation")
	}
}

func TestProviderMetadataRemainsCharged(t *testing.T) {
	old := maxRetainedStreamBytes
	maxRetainedStreamBytes = 2048
	t.Cleanup(func() { maxRetainedStreamBytes = old })
	s := NewStream(ProtoMessages, ProtoChat)
	defer s.Close()
	s.Consume([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"" + strings.Repeat("f", 1000) + "\"}]}\n\n"))
	if used := retainedStreamBytes.Load(); used < 1000 {
		t.Fatalf("finish metadata uncharged: %d", used)
	}
	s.Close()
	s = NewStream(ProtoChat, ProtoMessages)
	defer s.Close()
	s.Consume([]byte("event: " + strings.Repeat(" ", 1000) + "x\n"))
	if used := retainedStreamBytes.Load(); used != 1 {
		t.Fatalf("event retained backing bytes: %d", used)
	}
}
