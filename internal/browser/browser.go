package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"

	"github.com/wangnov/get-feishu-docs/internal/contracts"
)

const (
	managedChromiumRevision = launcher.RevisionDefault
	defaultOutputDir        = "./output"
)

type StartOptions struct {
	Mode        contracts.BrowserMode
	BrowserPath string
	Headless    bool
}

type BrowserSession struct {
	Browser  *rod.Browser
	Info     contracts.BrowserInfo
	mode     contracts.BrowserMode
	launcher *launcher.Launcher
}

type resolvedBrowser struct {
	info contracts.BrowserInfo
	mode contracts.BrowserMode
}

func (s *BrowserSession) Close() error {
	if s == nil {
		return nil
	}

	var closeErr error
	if s.Browser != nil {
		if err := s.Browser.Close(); err != nil {
			closeErr = err
		}
	}
	if s.launcher != nil {
		s.launcher.Kill()
	}
	return closeErr
}

func StartBrowser(ctx context.Context, opts StartOptions) (*BrowserSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	mode := opts.Mode
	if mode == "" {
		mode = contracts.BrowserModeAuto
	}
	if !mode.IsValid() {
		return nil, contracts.CLIError{
			Code:    contracts.ErrorCodeUsage,
			Message: "unsupported browser mode",
		}
	}

	resolved, err := resolveBrowserPath(ctx, mode, opts.BrowserPath)
	if err != nil {
		return nil, err
	}

	launch := launcher.New().Context(ctx)
	launch.Bin(resolved.info.Path)
	launch.Headless(opts.Headless)
	launch.Leakless(true)

	uri, err := launch.Launch()
	if err != nil {
		return nil, contracts.NewCLIError(
			contracts.ErrorCodeBrowserUnavailable,
			"failed to start browser",
			err,
		)
	}

	browser := rod.New().ControlURL(uri).Context(ctx)
	if err := browser.Connect(); err != nil {
		launch.Kill()
		return nil, contracts.NewCLIError(
			contracts.ErrorCodeBrowserUnavailable,
			"failed to connect to browser",
			err,
		)
	}

	return &BrowserSession{
		Browser:  browser,
		Info:     resolved.info,
		mode:     resolved.mode,
		launcher: launch,
	}, nil
}

func BrowserStatus() (contracts.BrowserStatusResult, error) {
	status := contracts.BrowserStatusResult{
		Strategy: string(contracts.BrowserModeAuto),
		System:   contracts.BrowserInfo{Name: "system"},
		Managed:  contracts.BrowserInfo{Name: "managed"},
	}

	systemInfo, systemErr := detectSystemBrowser("")
	status.System = systemInfo
	if systemErr != nil {
		status.System.Error = systemErr.Error()
	}

	managedInfo, managedErr := managedBrowserInfo()
	status.Managed = managedInfo
	if managedErr != nil {
		status.Managed.Error = managedErr.Error()
	}

	if status.System.Available {
		status.SelectedBrowser = "system"
		status.Ok = true
		return status, nil
	}

	if status.Managed.Available {
		status.SelectedBrowser = "managed"
		status.Ok = true
		return status, nil
	}

	errCode := contracts.ErrorCodeBrowserUnavailable
	status.Ok = false
	status.Error = &contracts.ErrorResult{
		Code:    string(errCode),
		Message: "no browser source is available",
	}
	return status, contracts.NewCLIError(errCode, status.Error.Message, nil)
}

func Doctor() (contracts.DoctorResult, error) {
	result := contracts.DoctorResult{
		OutputDir: defaultOutputDir,
		Browser: map[string]interface{}{
			"strategy": string(contracts.BrowserModeAuto),
		},
	}

	status, err := BrowserStatus()
	result.Browser["status"] = status
	if status.Ok {
		result.Browser["selected"] = status.SelectedBrowser
	}

	if err != nil {
		clErr := contracts.AsCLIError(err)
		result.Ok = false
		result.Error = &contracts.ErrorResult{
			Code:    string(clErr.Code),
			Message: clErr.Message,
		}
		return result, clErr
	}

	if err := ensureDirWritable(defaultOutputDir); err != nil {
		result.Ok = false
		result.OutputWritable = false
		result.Error = &contracts.ErrorResult{
			Code:    string(contracts.ErrorCodeWriteFailed),
			Message: err.Error(),
		}
		return result, contracts.CLIError{
			Code:    contracts.ErrorCodeWriteFailed,
			Message: err.Error(),
		}
	}
	result.OutputWritable = true

	managedRoot, managedRootErr := ManagedBrowserRoot()
	if managedRootErr == nil {
		if err := ensureDirWritable(managedRoot); err != nil {
			result.Notes = append(result.Notes, "managed browser directory is not writable")
		}
	} else {
		result.Notes = append(result.Notes, managedRootErr.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var startErr error
	switch status.SelectedBrowser {
	case "managed":
		var sess *BrowserSession
		sess, startErr = StartBrowser(ctx, StartOptions{
			Mode:        contracts.BrowserModeManaged,
			BrowserPath: status.Managed.Path,
			Headless:    true,
		})
		if startErr == nil {
			_ = sess.Close()
		}
	case "system":
		var sess *BrowserSession
		sess, startErr = StartBrowser(ctx, StartOptions{
			Mode:        contracts.BrowserModeSystem,
			BrowserPath: status.System.Path,
			Headless:    true,
		})
		if startErr == nil {
			_ = sess.Close()
		}
	}

	if startErr != nil {
		clErr := contracts.AsCLIError(startErr)
		result.Ok = false
		result.Error = &contracts.ErrorResult{
			Code:    string(clErr.Code),
			Message: clErr.Message,
		}
		return result, startErr
	}

	result.Ok = true
	return result, nil
}

func BrowserInstall() (contracts.BrowserInstallResult, error) {
	path, info, downloaded, err := managedBrowserBinary(context.Background())
	if err != nil {
		return contracts.BrowserInstallResult{
			Installed: false,
			Error: &contracts.ErrorResult{
				Code:    string(contracts.ErrorCodeBrowserInstall),
				Message: err.Error(),
			},
		}, contracts.NewCLIError(contracts.ErrorCodeBrowserInstall, "failed to install managed browser", err)
	}

	return contracts.BrowserInstallResult{
		Installed: downloaded,
		Version:   info.Version,
		Path:      path,
	}, nil
}

func resolveBrowserPath(ctx context.Context, mode contracts.BrowserMode, explicitPath string) (resolvedBrowser, error) {
	if strings.TrimSpace(explicitPath) != "" {
		info, err := detectSystemBrowser(explicitPath)
		if err != nil {
			return resolvedBrowser{}, err
		}
		return resolvedBrowser{
			info: info,
			mode: contracts.BrowserModeSystem,
		}, nil
	}

	switch mode {
	case contracts.BrowserModeSystem:
		info, err := detectSystemBrowser("")
		if err != nil {
			return resolvedBrowser{}, err
		}
		return resolvedBrowser{info: info, mode: contracts.BrowserModeSystem}, nil
	case contracts.BrowserModeManaged:
		path, info, _, err := managedBrowserBinary(ctx)
		if err != nil {
			return resolvedBrowser{}, err
		}
		info.Path = path
		return resolvedBrowser{info: info, mode: contracts.BrowserModeManaged}, nil
	case contracts.BrowserModeAuto:
		systemInfo, err := detectSystemBrowser("")
		if err == nil {
			return resolvedBrowser{info: systemInfo, mode: contracts.BrowserModeSystem}, nil
		}

		path, managedInfo, _, err := managedBrowserBinary(ctx)
		if err != nil {
			return resolvedBrowser{}, err
		}
		managedInfo.Path = path
		return resolvedBrowser{info: managedInfo, mode: contracts.BrowserModeManaged}, nil
	default:
		return resolvedBrowser{}, contracts.NewCLIError(contracts.ErrorCodeUsage, "invalid browser mode", nil)
	}
}

func managedBrowserBinary(ctx context.Context) (string, contracts.BrowserInfo, bool, error) {
	managed, err := managedBrowserConfig(ctx)
	if err != nil {
		return "", contracts.BrowserInfo{Name: "managed", Available: false, Error: err.Error()}, false, err
	}

	binPath := managed.BinPath()
	existed := true
	if _, err := os.Stat(binPath); err != nil {
		existed = false
	}

	resolved, err := managed.Get()
	if err != nil {
		return "", contracts.BrowserInfo{Name: "managed", Available: false, Error: err.Error()}, false, err
	}

	version, versionErr := browserVersion(resolved)
	if versionErr != nil {
		return "", contracts.BrowserInfo{Name: "managed", Available: false, Error: versionErr.Error()}, false, versionErr
	}

	return resolved, contracts.BrowserInfo{
		Name:      "managed",
		Path:      resolved,
		Version:   version,
		Available: true,
	}, !existed, nil
}

func managedBrowserInfo() (contracts.BrowserInfo, error) {
	managed, err := managedBrowserConfig(context.Background())
	if err != nil {
		return contracts.BrowserInfo{Name: "managed", Available: false, Error: err.Error()}, err
	}

	path := managed.BinPath()
	if _, err := os.Stat(path); err != nil {
		return contracts.BrowserInfo{
			Name:      "managed",
			Available: false,
			Error:     "managed browser binary not found",
		}, contracts.NewCLIError(contracts.ErrorCodeBrowserUnavailable, "managed browser binary not found", err)
	}

	if err := managed.Validate(); err != nil {
		return contracts.BrowserInfo{
			Name:      "managed",
			Available: false,
			Path:      path,
			Error:     err.Error(),
		}, contracts.NewCLIError(contracts.ErrorCodeBrowserUnavailable, "managed browser validation failed", err)
	}

	version, versionErr := browserVersion(path)
	if versionErr != nil {
		return contracts.BrowserInfo{
			Name:      "managed",
			Available: false,
			Path:      path,
			Error:     versionErr.Error(),
		}, contracts.NewCLIError(contracts.ErrorCodeBrowserUnavailable, "managed browser version check failed", versionErr)
	}

	return contracts.BrowserInfo{
		Name:      "managed",
		Available: true,
		Path:      path,
		Version:   version,
	}, nil
}

func detectSystemBrowser(explicitPath string) (contracts.BrowserInfo, error) {
	path := strings.TrimSpace(explicitPath)
	if path != "" {
		foundPath, err := exec.LookPath(path)
		if err != nil {
			return contracts.BrowserInfo{
				Name:      "system",
				Available: false,
				Error:     "system browser executable not found",
			}, contracts.NewCLIError(contracts.ErrorCodeBrowserUnavailable, "system browser executable not found", nil)
		}
		path = foundPath
	}

	if path == "" {
		found, has := launcher.LookPath()
		if !has {
			return contracts.BrowserInfo{
				Name:      "system",
				Available: false,
				Error:     "system browser executable not found",
			}, contracts.NewCLIError(contracts.ErrorCodeBrowserUnavailable, "system browser executable not found", nil)
		}
		path = found
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return contracts.BrowserInfo{Name: "system", Available: false, Error: err.Error()}, err
	}
	if _, err := os.Stat(absPath); err != nil {
		return contracts.BrowserInfo{
			Name:      "system",
			Available: false,
			Error:     err.Error(),
		}, contracts.NewCLIError(contracts.ErrorCodeBrowserUnavailable, "system browser not executable", err)
	}

	version, err := browserVersion(absPath)
	if err != nil {
		return contracts.BrowserInfo{
			Name:      "system",
			Available: false,
			Path:      absPath,
			Error:     err.Error(),
		}, contracts.NewCLIError(contracts.ErrorCodeBrowserUnavailable, "system browser version check failed", err)
	}

	return contracts.BrowserInfo{
		Name:      "system",
		Available: true,
		Version:   version,
		Path:      absPath,
	}, nil
}

func managedBrowserConfig(ctx context.Context) (*launcher.Browser, error) {
	root, err := ManagedBrowserRoot()
	if err != nil {
		return nil, err
	}

	managed := launcher.NewBrowser()
	managed.Context = ctx
	managed.RootDir = root
	managed.Revision = managedChromiumRevision
	managed.Hosts = []launcher.Host{
		launcher.HostPlaywright,
		launcher.HostGoogle,
		launcher.HostNPM,
	}
	return managed, nil
}

func browserVersion(binaryPath string) (string, error) {
	cmd := exec.Command(binaryPath, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("browser version command failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func ensureDirWritable(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}

	tf, err := os.CreateTemp(path, ".get-feishu-docs-writable")
	if err != nil {
		return err
	}
	_ = tf.Close()
	_ = os.Remove(tf.Name())
	return nil
}

func ManagedBrowserRoot() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to read user home: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "get-feishu-docs", "browser"), nil
	case "windows":
		localAppData := os.Getenv("LocalAppData")
		if localAppData == "" {
			return "", fmt.Errorf("LocalAppData environment variable not found")
		}
		return filepath.Join(localAppData, "get-feishu-docs", "browser"), nil
	default:
		return filepath.Join(homeDir, ".local", "share", "get-feishu-docs", "browser"), nil
	}
}
