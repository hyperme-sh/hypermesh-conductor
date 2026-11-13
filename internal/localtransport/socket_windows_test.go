//go:build windows

package localtransport

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func testPipePath(t *testing.T) string {
	t.Helper()
	prefix, err := pipePrefix()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s%d-%d", prefix, os.Getpid(), time.Now().UnixNano())
}

func TestPrivatePipeRoundTrip(t *testing.T) {
	path := testPipePath(t)
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := ValidateListener(listener); err != nil {
		t.Fatal(err)
	}
	if other, err := Listen(path); err == nil {
		other.Close()
		t.Fatal("replaced live pipe")
	}
	for i := 0; i < 2; i++ {
		done := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			data := make([]byte, 4)
			if _, err = io.ReadFull(conn, data); err == nil {
				if string(data) != "ping" {
					err = fmt.Errorf("unexpected request")
				} else {
					_, err = conn.Write([]byte("pong"))
				}
			}
			// Keep the pipe open until the client has consumed the response.
			if err == nil {
				_, err = io.Copy(io.Discard, conn)
			}
			done <- err
		}()
		conn, err := DialContext(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, 4)
		if _, err := io.ReadFull(conn, data); err != nil {
			t.Fatal(err)
		}
		if string(data) != "pong" {
			t.Fatal("unexpected response")
		}
		conn.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPipeDescriptorRefusesBroadOrForeignAccess(t *testing.T) {
	user, err := userSID()
	if err != nil {
		t.Fatal(err)
	}
	owner := "O:" + user.String()
	for _, descriptor := range []string{
		owner + "D:(A;;GA;;;WD)", owner + "D:NO_ACCESS_CONTROL",
		"O:SYD:(A;;GA;;;" + user.String() + ")",
		owner + "D:(D;;GA;;;WD)(A;;GA;;;" + user.String() + ")",
	} {
		sd, err := windows.SecurityDescriptorFromString(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		if err := privateDescriptor(sd); err == nil {
			t.Fatalf("accepted %s", descriptor)
		}
	}
}

func TestPipeRejectsDefaultACLBeforeRequest(t *testing.T) {
	path := testPipePath(t)
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if ValidateListener(listener) == nil {
		t.Fatal("accepted externally constructed listener")
	}
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		data := make([]byte, 1)
		n, err := conn.Read(data)
		if n != 0 {
			done <- fmt.Errorf("sent bytes before authentication")
			return
		}
		if err != io.EOF {
			done <- fmt.Errorf("expected closed connection, got %v", err)
			return
		}
		done <- nil
	}()
	if conn, err := DialContext(context.Background(), path); err == nil {
		conn.Close()
		t.Fatal("accepted default ACL")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPipePathAndCancellation(t *testing.T) {
	for _, path := range []string{`\\remote\pipe\anything`, `C:\pipe`, `\\.\pipe\other-user`, testPipePath(t) + `\child`} {
		if _, err := Listen(path); err == nil {
			t.Fatal("accepted invalid path")
		}
		if _, err := DialContext(context.Background(), path); err == nil {
			t.Fatal("dialed invalid path")
		}
	}
	t.Setenv("HYPERMESH_CONDUCTOR_SOCKET", "")
	t.Setenv("HYPERMESH_CONDUCTOR_INSTANCE", "valid-instance")
	path, err := DefaultPath()
	if err != nil || validatePipePath(path) != nil {
		t.Fatal(path, err)
	}
	t.Setenv("HYPERMESH_CONDUCTOR_INSTANCE", "../invalid")
	if _, err := DefaultPath(); err == nil {
		t.Fatal("accepted invalid instance")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DialContext(ctx, path); err == nil {
		t.Fatal("ignored cancellation")
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	if ValidateListener(tcp) == nil {
		t.Fatal("accepted TCP listener")
	}
}
