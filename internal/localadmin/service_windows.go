package localadmin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ziad-hsn/cpra/internal/installpath"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const windowsServiceName = "CPRa"

func nativePrepare(l installpath.Layout, account string) (string, error) {
	if l.Scope == "user" {
		if account != "" {
			return "", errors.New("Windows user mode uses the current user; omit --account")
		}
		return "", nil
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return "", errors.New("system installation requires an elevated administrator terminal")
	}
	manager, existing, err := openNativeService(l)
	if err == nil {
		existing.Close()
		manager.Disconnect()
		if _, err = installation(l); err != nil {
			return "", fmt.Errorf("existing SCM service is not owned by this local installation: %w", err)
		}
	} else if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return "", err
	}
	account, err = normalizeServiceAccount(account)
	if err != nil {
		return "", err
	}
	if _, _, _, err = windows.LookupSID("", account); err != nil {
		return "", fmt.Errorf("service account must already exist and resolve on this machine: %w", err)
	}
	return account, nil
}

// Before registration, configuration is restricted to administrators and SYSTEM.
// Once registered, only the service SID receives access, not all LocalService processes.
func nativeSecure(l installpath.Layout, _ string) error {
	var principal string
	if l.Scope == "system" {
		sid, _, _, err := windows.LookupSID("", `NT SERVICE\`+windowsServiceName)
		if err != nil && !errors.Is(err, windows.ERROR_NONE_MAPPED) {
			return fmt.Errorf("resolve service SID: %w", err)
		}
		if err == nil {
			principal = sid.String()
		}
	} else {
		token := windows.GetCurrentProcessToken()
		user, err := token.GetTokenUser()
		if err != nil {
			return err
		}
		principal = user.User.Sid.String()
	}
	for _, entry := range []struct {
		path  string
		write bool
	}{{l.ConfigDir, false}, {l.BinDir, false}, {l.StateDir, true}, {l.LogDir, true}} {
		if entry.path == "" {
			continue
		}
		if _, err := os.Lstat(entry.path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		sd, err := windows.SecurityDescriptorFromString(serviceDACL(principal, entry.write || l.Scope == "user"))
		if err != nil {
			return err
		}
		acl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		err = filepath.WalkDir(entry.path, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing ACL update through link %s", path)
			}
			return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func openNativeService(l installpath.Layout) (*mgr.Mgr, *mgr.Service, error) {
	if l.Scope != "system" {
		return nil, nil, errors.New("Windows user mode runs in the foreground; SCM services require system scope")
	}
	manager, err := mgr.Connect()
	if err != nil {
		return nil, nil, err
	}
	service, err := manager.OpenService(windowsServiceName)
	if err != nil {
		manager.Disconnect()
		return nil, nil, err
	}
	return manager, service, nil
}
func nativeRunning(ctx context.Context, l installpath.Layout) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if l.Scope != "system" {
		return false, errors.New("Windows user mode runs in the foreground; SCM status requires system scope")
	}
	handle, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, err
	}
	manager := &mgr.Mgr{Handle: handle}
	name, _ := windows.UTF16PtrFromString(windowsServiceName)
	serviceHandle, err := windows.OpenService(handle, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		manager.Disconnect()
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return false, nil
		}
		return false, err
	}
	service := &mgr.Service{Name: windowsServiceName, Handle: serviceHandle}
	defer manager.Disconnect()
	defer service.Close()
	status, err := service.Query()
	return status.State != svc.Stopped, err
}
func nativeRegister(ctx context.Context, l installpath.Layout, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.Scope != "system" {
		return errors.New("Windows user mode runs in the foreground; no user service is registered")
	}
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	cfg := mgr.Config{ServiceType: windows.SERVICE_WIN32_OWN_PROCESS, StartType: mgr.StartAutomatic, ErrorControl: mgr.ErrorNormal, DisplayName: windowsServiceName, Description: "CPRa monitoring and recovery controller", ServiceStartName: account, SidType: windows.SERVICE_SID_TYPE_UNRESTRICTED}
	service, err := manager.OpenService(windowsServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		service, err = manager.CreateService(windowsServiceName, binaryPath(l), cfg, serviceArgs(l)...)
	} else if err == nil {
		if _, err = installation(l); err != nil {
			service.Close()
			return fmt.Errorf("existing SCM service has no valid local ownership record: %w", err)
		}
		status, e := service.Query()
		if e != nil {
			service.Close()
			return e
		}
		if status.State != svc.Stopped {
			service.Close()
			return errors.New("service must be stopped before updating registration")
		}
		cfg, err = service.Config()
		if err != nil {
			service.Close()
			return err
		}
		cfg = updatedServiceConfig(cfg, binaryPath(l), serviceArgs(l))
		err = service.UpdateConfig(cfg)
	}
	if err != nil {
		if service != nil {
			service.Close()
		}
		return err
	}
	defer service.Close()
	// Bound automatic crash recovery. A normal controlled stop is not restarted.
	return service.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}, {Type: mgr.ServiceRestart, Delay: 15 * time.Second}, {Type: mgr.NoAction}}, 86400)
}

// Preserve operator-managed start mode, delayed start, dependencies and account
// during an artifact update. An empty ServiceStartName asks ChangeServiceConfig
// to keep the existing identity, including an externally configured password.
func updatedServiceConfig(current mgr.Config, binary string, args []string) mgr.Config {
	current.BinaryPathName = serviceCommandLine(binary, args)
	current.ServiceStartName = ""
	current.Password = ""
	current.SidType = windows.SERVICE_SID_TYPE_UNRESTRICTED
	return current
}
func waitNativeState(ctx context.Context, service *mgr.Service, want svc.State) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State == want {
			return nil
		}
		if want == svc.Running && status.State == svc.Stopped {
			return fmt.Errorf("service stopped during startup (Win32=%d service=%d)", status.Win32ExitCode, status.ServiceSpecificExitCode)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func nativeStop(ctx context.Context, l installpath.Layout) error {
	manager, service, err := openNativeService(l)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if _, err = service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
	}
	return waitNativeState(ctx, service, svc.Stopped)
}
func nativeStart(ctx context.Context, l installpath.Layout) error {
	manager, service, err := openNativeService(l)
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Running {
		return nil
	}
	if status.State == svc.Stopped {
		if err = service.Start(); err != nil {
			return err
		}
	}
	return waitNativeState(ctx, service, svc.Running)
}
func nativeUnregister(ctx context.Context, l installpath.Layout) error {
	if err := nativeStop(ctx, l); err != nil {
		return err
	}
	manager, service, err := openNativeService(l)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer service.Close()
	return service.Delete()
}

// Protect ownership explicitly on every managed object; inherited permissive
// parent ACLs cannot grant other users access to manifests or provider secrets.
func serviceDACL(principal string, writable bool) string {
	descriptor := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if principal == "" {
		return descriptor
	}
	rights := "FRFX"
	if writable {
		rights = "0x1301bf"
	} // file modify, without WRITE_DAC/WRITE_OWNER
	return descriptor + "(A;OICI;" + rights + ";;;" + principal + ")"
}
func serviceCommandLine(binary string, args []string) string {
	parts := []string{syscall.EscapeArg(binary)}
	for _, arg := range args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

func normalizeServiceAccount(account string) (string, error) {
	if account == "" || strings.EqualFold(account, `NT AUTHORITY\LocalService`) {
		return `NT AUTHORITY\LocalService`, nil
	}
	if strings.EqualFold(account, `NT AUTHORITY\NetworkService`) {
		return `NT AUTHORITY\NetworkService`, nil
	}
	// A gMSA password is managed by Windows. The account must already be
	// provisioned and authorized for this host before installation.
	domain, name, ok := strings.Cut(account, `\`)
	if ok && domain != "" && len(name) > 1 && strings.HasSuffix(name, "$") && !strings.ContainsAny(account, " /\r\n\x00\t") && !strings.Contains(name, `\`) {
		return account, nil
	}
	return "", errors.New("supported Windows service accounts: LocalService, NetworkService, or an existing DOMAIN\\gMSA$; password-based accounts require explicit external SCM configuration")
}
