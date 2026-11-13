//go:build windows

package localtransport

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func userSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func pipePrefix() (string, error) {
	sid, err := userSID()
	if err != nil {
		return "", err
	}
	return `\\.\pipe\hypermesh-conductor-` + sid.String() + "-", nil
}

func validInstance(instance string) bool {
	if len(instance) == 0 || len(instance) > 64 {
		return false
	}
	for _, ch := range instance {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
			return false
		}
	}
	return true
}

func validatePipePath(path string) error {
	prefix, err := pipePrefix()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(path, prefix) || !validInstance(strings.TrimPrefix(path, prefix)) {
		return fmt.Errorf("Conductor pipe must be a local current-user instance")
	}
	return nil
}

func DefaultPath() (string, error) {
	if path := os.Getenv("HYPERMESH_CONDUCTOR_SOCKET"); path != "" {
		return path, validatePipePath(path)
	}
	instance := os.Getenv("HYPERMESH_CONDUCTOR_INSTANCE")
	if instance == "" {
		instance = "default"
	}
	if !validInstance(instance) {
		return "", fmt.Errorf("invalid Conductor pipe instance")
	}
	prefix, err := pipePrefix()
	return prefix + instance, err
}

// pipeListener can only be constructed after creating an exclusive, local-only
// pipe with a protected current-user/System DACL. Its name is routing, not auth.
type pipeListener struct{ net.Listener }

func Listen(path string) (net.Listener, error) {
	if err := validatePipePath(path); err != nil {
		return nil, err
	}
	sid, err := userSID()
	if err != nil {
		return nil, err
	}
	descriptor := "O:" + sid.String() + "D:P(A;;GA;;;" + sid.String() + ")(A;;GA;;;SY)"
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: descriptor, InputBufferSize: 65536, OutputBufferSize: 65536})
	if err != nil {
		return nil, err
	}
	return &pipeListener{listener}, nil
}

func (listener *pipeListener) Accept() (net.Conn, error) {
	conn, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if err := privatePipe(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func ValidateListener(listener net.Listener) error {
	if _, ok := listener.(*pipeListener); !ok {
		return fmt.Errorf("listener was not created as a private Conductor pipe")
	}
	return validatePipePath(listener.Addr().String())
}

func DialContext(ctx context.Context, path string) (net.Conn, error) {
	if err := validatePipePath(path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// go-winio opens with SECURITY_SQOS_PRESENT | SECURITY_ANONYMOUS, so
	// an untrusted server cannot impersonate this process during authentication.
	conn, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := privatePipe(conn); err != nil {
		conn.Close()
		return nil, err
	}
	if err := privateServer(windows.Handle(conn.(interface{ Fd() uintptr }).Fd())); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func privatePipe(conn net.Conn) error {
	raw, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("pipe handle unavailable")
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(raw.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	runtime.KeepAlive(conn)
	if err != nil {
		return err
	}
	return privateDescriptor(sd)
}

func privateDescriptor(sd *windows.SECURITY_DESCRIPTOR) error {
	current, err := userSID()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(current) {
		return fmt.Errorf("Conductor pipe belongs to another user")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("Conductor pipe has no restrictive DACL")
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		// Reject unfamiliar ACE forms, including object and callback grants.
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < 16 {
			return fmt.Errorf("unsupported Conductor pipe ACE")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || sid.Len() > int(ace.Header.AceSize)-8 {
			return fmt.Errorf("invalid Conductor pipe trustee")
		}
		if !sid.Equals(current) && !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			return fmt.Errorf("Conductor pipe grants another user access")
		}
	}
	runtime.KeepAlive(sd)
	return nil
}

// Same SID is not enough: sandboxed low-integrity processes can create pipes.
func privateServer(pipe windows.Handle) error {
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(pipe, &pid); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	if err := trustedServerToken(token); err != nil {
		return err
	}
	var checkedPID uint32
	if err := windows.GetNamedPipeServerProcessId(pipe, &checkedPID); err != nil {
		return err
	}
	status, err := windows.WaitForSingleObject(process, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) || checkedPID != pid {
		return fmt.Errorf("Conductor pipe server changed or exited")
	}
	return nil
}

func trustedServerToken(token windows.Token) error {
	current, err := userSID()
	if err != nil {
		return err
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	if !user.User.Sid.Equals(current) {
		return fmt.Errorf("Conductor server belongs to another user")
	}
	restricted, err := token.IsRestricted()
	if err != nil {
		return err
	}
	if restricted {
		return fmt.Errorf("Conductor server has a restricted token")
	}
	serverLevel, err := integrityLevel(token)
	if err != nil {
		return err
	}
	clientLevel, err := integrityLevel(windows.GetCurrentProcessToken())
	if err != nil {
		return err
	}
	if serverLevel < clientLevel || serverLevel < 0x2000 {
		return fmt.Errorf("Conductor server has lower integrity")
	}
	return nil
}

func integrityLevel(token windows.Token) (uint32, error) {
	var size uint32
	err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, nil, 0, &size)
	if err != windows.ERROR_INSUFFICIENT_BUFFER || size < uint32(unsafe.Sizeof(windows.Tokenmandatorylabel{})) || size > 65536 {
		return 0, fmt.Errorf("cannot inspect process integrity")
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, &buffer[0], size, &size); err != nil {
		return 0, err
	}
	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buffer[0]))
	sid := label.Label.Sid
	if sid == nil || !sid.IsValid() || sid.SubAuthorityCount() != 1 || !strings.HasPrefix(sid.String(), "S-1-16-") {
		return 0, fmt.Errorf("invalid process integrity label")
	}
	level := sid.SubAuthority(0)
	runtime.KeepAlive(buffer)
	return level, nil
}
