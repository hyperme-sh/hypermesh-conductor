package capabilitydispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

// IPC authenticates the controller/bridge process through kernel credentials.
// Production construction always selects UID zero; there is no header fallback.
type IPC struct {
	socket string
	uid    uint32
}

func RootIPC(socket string) (IPC, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return IPC{}, Refused
	}
	return IPC{socket: socket, uid: 0}, nil
}
func (p IPC) post(ctx context.Context, path string, body []byte, limit int, out any) error {
	return p.request(ctx, "POST", path, body, limit, out)
}
func (p IPC) request(ctx context.Context, method, path string, body []byte, limit int, out any) error {
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", p.socket)
		if err != nil {
			return nil, Refused
		}
		if checkPeer(conn, p.uid) != nil {
			conn.Close()
			return nil, Refused
		}
		return conn, nil
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, bytes.NewReader(body))
	if err != nil {
		return Refused
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Refused
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Refused
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return Refused
	}
	if out == nil {
		return nil
	}
	return Decode(raw, out)
}
func (p IPC) Review(ctx context.Context, raw []byte) (Decision, error) {
	var result Decision
	err := p.post(ctx, "/review", raw, 4096, &result)
	return result, err
}
func (p IPC) Prepare(ctx context.Context, request Preparation) (Reservation, error) {
	var result Reservation
	raw, err := json.Marshal(request)
	if err != nil {
		return result, Refused
	}
	err = p.post(ctx, "/prepare", raw, 65536, &result)
	return result, err
}
func (p IPC) Execute(ctx context.Context, request Execution) (Result, error) {
	var result Result
	raw, err := json.Marshal(request)
	if err != nil {
		return result, Refused
	}
	err = p.post(ctx, "/execute", raw, 6*1048576+4096, &result)
	return result, err
}
func (p IPC) Cancel(ctx context.Context, id, permit string) error {
	raw, _ := json.Marshal(map[string]string{"call_id": id, "permit": permit})
	return p.post(ctx, "/cancel", raw, 4096, nil)
}

func Decode(raw []byte, out any) error {
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 16 {
			return Refused
		}
		token, err := scan.Token()
		if err != nil {
			return Refused
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		seen := map[string]bool{}
		count := 0
		for scan.More() {
			count++
			if count > 256 {
				return Refused
			}
			if delimiter == '{' {
				key, err := scan.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return Refused
				}
				seen[name] = true
			}
			if value(depth+1) != nil {
				return Refused
			}
		}
		close, err := scan.Token()
		if err != nil || !((delimiter == '{' && close == json.Delim('}')) || (delimiter == '[' && close == json.Delim(']'))) {
			return Refused
		}
		return nil
	}
	if value(0) != nil {
		return Refused
	}
	if _, err := scan.Token(); err != io.EOF {
		return Refused
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return Refused
	}
	return nil
}

func (p IPC) bindings(ctx context.Context) ([]Call, error) {
	var reply struct {
		Schema  int    `json:"schema"`
		Enabled []Call `json:"enabled"`
	}
	if p.request(ctx, "GET", "/capabilities", nil, 65536, &reply) != nil || reply.Schema != 1 || len(reply.Enabled) > 64 {
		return nil, Refused
	}
	return reply.Enabled, nil
}
