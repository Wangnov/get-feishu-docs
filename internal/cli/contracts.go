package cli

import (
	"fmt"
	"io"
	"strings"

	cn "github.com/wangnov/get-feishu-docs/internal/contracts"
)

type ErrorCode = cn.ErrorCode

const (
	ErrorCodeBrowserUnavailable = cn.ErrorCodeBrowserUnavailable
	ErrorCodeBrowserInstall     = cn.ErrorCodeBrowserInstall
	ErrorCodePasswordRequired   = cn.ErrorCodePasswordRequired
	ErrorCodeDocumentTimeout    = cn.ErrorCodeDocumentTimeout
	ErrorCodeSelectorDrift      = cn.ErrorCodeSelectorDrift
	ErrorCodeCaptureFailed      = cn.ErrorCodeCaptureFailed
	ErrorCodeWriteFailed        = cn.ErrorCodeWriteFailed
	ErrorCodeUsage              = cn.ErrorCodeUsage
	ErrorCodeUnknown            = cn.ErrorCodeUnknown
)

const (
	ProgressText = cn.ProgressText
	ProgressJSON = cn.ProgressJSON
)

type BrowserMode = cn.BrowserMode

const (
	BrowserAuto    BrowserMode = cn.BrowserModeAuto
	BrowserSystem  BrowserMode = cn.BrowserModeSystem
	BrowserManaged BrowserMode = cn.BrowserModeManaged
)

type ExitCode = cn.ExitCode

const (
	ExitOK      = cn.ExitOK
	ExitUsage   = cn.ExitUsage
	ExitBrowser = cn.ExitBrowser
	ExitAuth    = cn.ExitAuth
	ExitCapture = cn.ExitCapture
	ExitWrite   = cn.ExitWrite
)

type CLIError = cn.CLIError

type ProgressEvent = cn.ProgressEvent

type FileArtifact = cn.FileArtifact

type Result = cn.Result

type ErrorResult = cn.ErrorResult

type CaptureRequest = cn.CaptureRequest

type CaptureHandler = cn.CaptureHandler

type DoctorResult = cn.DoctorResult

type BrowserInfo = cn.BrowserInfo

type BrowserStatusResult = cn.BrowserStatusResult

type BrowserInstallResult = cn.BrowserInstallResult

type Handlers struct {
	Capture        CaptureHandler
	Doctor         func() (DoctorResult, error)
	BrowserStatus  func() (BrowserStatusResult, error)
	BrowserInstall func() (BrowserInstallResult, error)
}

type Config struct {
	OutWriter          io.Writer
	ErrWriter          io.Writer
	InReader           io.Reader
	Handlers           Handlers
	ParseOutputFormats func(string, bool) ([]string, error)
	ResolvePassword    func(string, bool, io.Reader, io.Writer, bool) (string, error)
}

type OutputFormat string

const (
	FormatHTML OutputFormat = "html"
	FormatPNG  OutputFormat = "png"
	FormatPDF  OutputFormat = "pdf"
	FormatMD   OutputFormat = "md"
)

func (c Config) withDefaults() Config {
	result := c
	if result.Handlers.Capture == nil {
		result.Handlers.Capture = defaultCaptureHandler
	}
	if result.Handlers.Doctor == nil {
		result.Handlers.Doctor = defaultDoctorHandler
	}
	if result.Handlers.BrowserStatus == nil {
		result.Handlers.BrowserStatus = defaultBrowserStatusHandler
	}
	if result.Handlers.BrowserInstall == nil {
		result.Handlers.BrowserInstall = defaultBrowserInstallHandler
	}
	if result.ParseOutputFormats == nil {
		result.ParseOutputFormats = ParseOutputFormats
	}
	if result.ResolvePassword == nil {
		result.ResolvePassword = ResolvePassword
	}
	return result
}

func ExitForError(err error) ExitCode {
	return cn.ExitForError(err)
}

func defaultCaptureHandler(_ CaptureRequest, _ func(ProgressEvent)) (Result, error) {
	err := CLIError{Code: ErrorCodeCaptureFailed, Message: "capture engine not linked in this build"}
	return Result{
		Ok: false,
		Error: &ErrorResult{
			Code:    string(err.Code),
			Message: err.Message,
		},
	}, err
}

func defaultDoctorHandler() (DoctorResult, error) {
	err := CLIError{Code: ErrorCodeUsage, Message: "doctor handler not linked in this build"}
	return DoctorResult{}, err
}

func defaultBrowserStatusHandler() (BrowserStatusResult, error) {
	err := CLIError{Code: ErrorCodeUsage, Message: "browser status handler not linked in this build"}
	return BrowserStatusResult{}, err
}

func defaultBrowserInstallHandler() (BrowserInstallResult, error) {
	err := CLIError{Code: ErrorCodeUsage, Message: "browser install handler not linked in this build"}
	return BrowserInstallResult{}, err
}

func ParseOutputFormats(raw string, outputAll bool) ([]string, error) {
	if outputAll {
		return []string{
			string(FormatHTML),
			string(FormatPNG),
			string(FormatPDF),
			string(FormatMD),
		}, nil
	}
	if strings.TrimSpace(raw) == "" {
		return []string{string(FormatHTML)}, nil
	}

	parts := strings.Split(raw, ",")
	seen := map[string]struct{}{}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		p := strings.TrimSpace(strings.ToLower(part))
		if p == "" {
			continue
		}
		switch OutputFormat(p) {
		case FormatHTML, FormatPNG, FormatPDF, FormatMD:
			if _, exists := seen[p]; exists {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		default:
			return nil, fmt.Errorf("invalid output format: %s", p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("output format is empty")
	}
	return out, nil
}

func ValidateBrowserMode(mode string) (BrowserMode, error) {
	return cn.ValidateBrowserMode(mode)
}

func ValidateProgressMode(mode string) error {
	switch mode {
	case ProgressText, ProgressJSON:
		return nil
	default:
		return fmt.Errorf("invalid progress mode: %s", mode)
	}
}
