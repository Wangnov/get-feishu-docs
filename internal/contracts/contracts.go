package contracts

import (
	"fmt"
	"strings"
)

type ErrorCode string

const (
	ErrorCodeBrowserUnavailable ErrorCode = "browser_unavailable"
	ErrorCodeBrowserInstall     ErrorCode = "browser_install_failed"
	ErrorCodePasswordRequired   ErrorCode = "password_required"
	ErrorCodeDocumentTimeout    ErrorCode = "document_timeout"
	ErrorCodeSelectorDrift      ErrorCode = "document_selector_drift"
	ErrorCodeCaptureFailed      ErrorCode = "capture_failed"
	ErrorCodeWriteFailed        ErrorCode = "write_failed"
	ErrorCodeUsage              ErrorCode = "usage"
	ErrorCodeUnknown            ErrorCode = "unknown"
)

const (
	ProgressText = "text"
	ProgressJSON = "jsonl"
)

type BrowserMode string

const (
	BrowserModeAuto    BrowserMode = "auto"
	BrowserModeSystem  BrowserMode = "system"
	BrowserModeManaged BrowserMode = "managed"
)

type ExitCode int

const (
	ExitOK ExitCode = iota
	ExitUsage
	ExitBrowser
	ExitAuth
	ExitCapture
	ExitWrite
)

func (mode BrowserMode) IsValid() bool {
	switch mode {
	case BrowserModeAuto, BrowserModeSystem, BrowserModeManaged:
		return true
	default:
		return false
	}
}

type CLIError struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e CLIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Code)
}

func (e CLIError) Unwrap() error {
	return e.Err
}

func NewCLIError(code ErrorCode, msg string, err error) CLIError {
	if strings.TrimSpace(msg) == "" && err != nil {
		msg = strings.TrimSpace(err.Error())
	}
	return CLIError{Code: code, Message: msg, Err: err}
}

func AsCLIError(err error) CLIError {
	if err == nil {
		return CLIError{Code: ErrorCodeUnknown, Message: "no error"}
	}
	if ce, ok := err.(CLIError); ok {
		return ce
	}
	return CLIError{Code: ErrorCodeCaptureFailed, Message: err.Error()}
}

func ExitForError(err error) ExitCode {
	if err == nil {
		return ExitOK
	}

	clErr := AsCLIError(err)
	switch clErr.Code {
	case ErrorCodeUsage:
		return ExitUsage
	case ErrorCodeBrowserUnavailable, ErrorCodeBrowserInstall:
		return ExitBrowser
	case ErrorCodePasswordRequired:
		return ExitAuth
	case ErrorCodeWriteFailed:
		return ExitWrite
	default:
		return ExitCapture
	}
}

func ValidateBrowserMode(raw string) (BrowserMode, error) {
	mode := BrowserMode(strings.ToLower(strings.TrimSpace(raw)))
	if !mode.IsValid() {
		if mode == "" {
			return BrowserModeAuto, nil
		}
		return BrowserModeAuto, fmt.Errorf("unsupported browser mode: %s", raw)
	}
	return mode, nil
}

type ProgressEvent struct {
	Type      string         `json:"type"`
	Stage     string         `json:"stage"`
	Message   string         `json:"message"`
	Stats     map[string]int `json:"stats,omitempty"`
	Timestamp string         `json:"timestamp"`
}

type FileArtifact struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type Result struct {
	Ok              bool                   `json:"ok"`
	Title           string                 `json:"title,omitempty"`
	TitleRaw        string                 `json:"titleRaw,omitempty"`
	OutputDir       string                 `json:"outputDir,omitempty"`
	SelectedOutputs []string               `json:"selectedOutputs,omitempty"`
	Files           []FileArtifact         `json:"files,omitempty"`
	Stats           map[string]int         `json:"stats,omitempty"`
	Debug           map[string]interface{} `json:"debug,omitempty"`
	Error           *ErrorResult           `json:"error,omitempty"`
}

type ErrorResult struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type CaptureRequest struct {
	URL         string
	Password    string
	OutDir      string
	Outputs     []string
	BrowserMode BrowserMode
	BrowserPath string
	TimeoutMS   int
	SettleMS    int
	Debug       bool
}

type CaptureHandler func(CaptureRequest, func(ProgressEvent)) (Result, error)

type DoctorResult struct {
	Ok             bool                   `json:"ok"`
	Browser        map[string]interface{} `json:"browser"`
	Notes          []string               `json:"notes,omitempty"`
	OutputDir      string                 `json:"outputDir,omitempty"`
	OutputWritable bool                   `json:"outputWritable"`
	Error          *ErrorResult           `json:"error,omitempty"`
}

type BrowserInfo struct {
	Name      string `json:"name,omitempty"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

type BrowserStatusResult struct {
	Ok              bool         `json:"ok"`
	Strategy        string       `json:"strategy"`
	System          BrowserInfo  `json:"system"`
	Managed         BrowserInfo  `json:"managed"`
	SelectedBrowser string       `json:"selectedBrowser"`
	Error           *ErrorResult `json:"error,omitempty"`
}

type BrowserInstallResult struct {
	Installed bool         `json:"installed"`
	Version   string       `json:"version,omitempty"`
	Path      string       `json:"path,omitempty"`
	Error     *ErrorResult `json:"error,omitempty"`
}
