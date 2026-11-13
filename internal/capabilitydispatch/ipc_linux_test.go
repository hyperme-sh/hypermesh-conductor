//go:build linux

package capabilitydispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIPCChecksKernelPeerAndRejectsSpoofedController(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"protocol_version": 1, "call_id": "call", "call_sha256": "hash", "decision": "allow", "expires_at": 1})
	})}
	go server.Serve(listener)
	defer server.Close()
	client := IPC{socket: path, uid: uint32(os.Geteuid()) + 1}
	if _, err = client.Review(context.Background(), []byte(`{}`)); err == nil || calls.Load() != 0 {
		t.Fatal("wrong controller peer accepted")
	}
	client.uid = uint32(os.Geteuid())
	if _, err = client.Review(context.Background(), []byte(`{}`)); err != nil || calls.Load() != 1 {
		t.Fatal("owned fixture peer refused", err)
	}
}
func TestStrictProtocolRejectsNestedDuplicateFields(t *testing.T) {
	for _, raw := range []string{`{"protocol_version":1,"protocol_version":1}`, `{"limits":[{"id":"one","id":"two"}]}`, `{} {}`, `{"unknown":true}`} {
		var result Reservation
		if Decode([]byte(raw), &result) == nil {
			t.Fatal("malformed controller reply accepted")
		}
	}
}

func TestExecuteAcceptsEscapedOutputWithinDecodedLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	output := `"` + strings.Repeat("<", 1048574) + `"`
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Result{Protocol: 1, CallID: "call", CallHash: "hash", Completed: true, Output: output})
	})}
	go server.Serve(listener)
	defer server.Close()
	client := IPC{socket: path, uid: uint32(os.Geteuid())}
	result, err := client.Execute(context.Background(), Execution{})
	if err != nil || result.Output != output {
		t.Fatalf("valid escaped output refused: %v", err)
	}
}

func TestCallWireAllowsEscapingButBoundsDecodedArguments(t *testing.T) {
	for _, size := range []int{65534, 65535} {
		args := `"` + strings.Repeat("<", size) + `"`
		raw, _ := json.Marshal(Call{Arguments: args})
		call, err := ReadCall(bytes.NewReader(raw))
		if size == 65534 && (err != nil || call.Arguments != args) {
			t.Fatal("valid escaped arguments refused")
		}
		if size == 65535 && err == nil {
			t.Fatal("oversized decoded arguments accepted")
		}
	}
	if _, err := ReadCall(strings.NewReader(strings.Repeat(" ", 6*65536+4097))); err == nil {
		t.Fatal("oversized wire accepted")
	}
}
