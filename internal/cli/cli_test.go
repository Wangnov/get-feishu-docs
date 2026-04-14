package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestParseOutputFormats(t *testing.T) {
	t.Parallel()

	out, err := ParseOutputFormats("html,png", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out[0] != "html" || out[1] != "png" {
		t.Fatalf("unexpected formats: %v", out)
	}

	out, err = ParseOutputFormats("png,html,png", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out[0] != "png" || out[1] != "html" {
		t.Fatalf("dedupe failed: %v", out)
	}

	out, err = ParseOutputFormats("pdf,unknown", false)
	if err == nil {
		t.Fatalf("expected error for invalid format, got %v", out)
	}

	out, err = ParseOutputFormats("", true)
	if err != nil {
		t.Fatalf("unexpected error for output-all: %v", err)
	}
	if len(out) != 4 || out[0] != "html" || out[3] != "md" {
		t.Fatalf("output-all mismatch: %v", out)
	}
}

func TestResolvePasswordPriority(t *testing.T) {
	t.Parallel()

	v, err := ResolvePassword("flag-pass", true, strings.NewReader("stdin-pass\n"), bytes.NewBuffer(nil), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "flag-pass" {
		t.Fatalf("expected flag password, got %q", v)
	}

	v, err = ResolvePassword("", true, strings.NewReader("stdin-pass\n"), bytes.NewBuffer(nil), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "stdin-pass" {
		t.Fatalf("expected stdin password, got %q", v)
	}
}

func TestRootAndCaptureCommandsUseSameFlow(t *testing.T) {
	t.Parallel()

	makeCommand := func(args []string) (*CaptureRequest, *bytes.Buffer, *bytes.Buffer, error) {
		request := &CaptureRequest{}
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		passwordProvided := "pw"
		cmd := NewRootCommand(Config{
			OutWriter: stdout,
			ErrWriter: stderr,
			InReader:  strings.NewReader(""),
			Handlers: Handlers{
				Capture: func(r CaptureRequest, emit func(ProgressEvent)) (Result, error) {
					copyReq := r
					request = &copyReq
					emit(ProgressEvent{Type: "step", Stage: "capture", Message: "running"})
					return Result{
						Ok:              true,
						Title:           "Demo Doc",
						TitleRaw:        "Demo Doc",
						OutputDir:       "/tmp/doc-output",
						SelectedOutputs: r.Outputs,
						Files: []FileArtifact{
							{Kind: "html", Path: "/tmp/doc-output/replica.html"},
						},
					}, nil
				},
			},
			ParseOutputFormats: ParseOutputFormats,
			ResolvePassword: func(string, bool, io.Reader, io.Writer, bool) (string, error) {
				return passwordProvided, nil
			},
		})
		cmd.SetArgs(args)
		cmd.SetOut(stdout)
		cmd.SetErr(stderr)
		err := cmd.Execute()
		return request, stdout, stderr, err
	}

	request, stdout, _, err := makeCommand([]string{"--json", "https://feishu.com/doc"})
	if err != nil {
		t.Fatalf("root command failed: %v", err)
	}
	if got := len(request.Outputs); got != 1 || request.Outputs[0] != "html" {
		t.Fatalf("root outputs mismatch: %v", request.Outputs)
	}
	if !jsonContains(stdout.String(), `"ok":true`) {
		t.Fatalf("expected json summary on stdout: %s", stdout.String())
	}

	request2, _, _, err := makeCommand([]string{"capture", "--output=html,png", "https://feishu.com/doc"})
	if err != nil {
		t.Fatalf("capture alias failed: %v", err)
	}
	if len(request2.Outputs) != 2 || request2.Outputs[0] != "html" || request2.Outputs[1] != "png" {
		t.Fatalf("capture outputs mismatch: %v", request2.Outputs)
	}
}

func TestOutputAllWinsOverOutput(t *testing.T) {
	t.Parallel()
	request, _, _, err := runCaptureWithArgs([]string{"--output-all", "--output=png", "https://feishu.com/doc"})
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}
	if len(request.Outputs) != 4 {
		t.Fatalf("expected output-all to expand to 4 formats, got %v", request.Outputs)
	}
}

func TestHelpContainsExamples(t *testing.T) {
	t.Parallel()

	cmd := NewRootCommand(Config{
		OutWriter: &bytes.Buffer{},
		ErrWriter: &bytes.Buffer{},
		InReader:  strings.NewReader(""),
	})
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help command failed: %v", err)
	}
	if !strings.Contains(cmd.OutOrStdout().(*bytes.Buffer).String(), "get-feishu-docs --json --progress=jsonl") {
		t.Fatalf("help missing expected JSON example")
	}
}

func TestProgressJSONL(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := NewRootCommand(Config{
		OutWriter: stdout,
		ErrWriter: stderr,
		InReader:  strings.NewReader(""),
		Handlers: Handlers{
			Capture: func(_ CaptureRequest, emit func(ProgressEvent)) (Result, error) {
				emit(ProgressEvent{Type: "step", Stage: "capture", Message: "running"})
				return Result{Ok: true}, nil
			},
		},
		ResolvePassword: func(string, bool, io.Reader, io.Writer, bool) (string, error) {
			return "x", nil
		},
	})
	cmd.SetArgs([]string{"--json", "--progress=jsonl", "https://feishu.com/doc"})
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}

	lines := nonEmptyLines(stderr.String())
	if len(lines) < 2 {
		t.Fatalf("expected progress events, got %d lines: %s", len(lines), stderr.String())
	}
	for _, line := range lines {
		var evt ProgressEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("invalid json progress line: %s, err=%v", line, err)
		}
		if evt.Type == "" || evt.Stage == "" || evt.Message == "" || evt.Timestamp == "" {
			t.Fatalf("missing field in progress event: %+v", evt)
		}
	}
}

func runCaptureWithArgs(args []string) (*CaptureRequest, *bytes.Buffer, *bytes.Buffer, error) {
	request := &CaptureRequest{}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := NewRootCommand(Config{
		OutWriter: stdout,
		ErrWriter: stderr,
		InReader:  strings.NewReader(""),
		Handlers: Handlers{
			Capture: func(r CaptureRequest, emit func(ProgressEvent)) (Result, error) {
				copyReq := r
				request = &copyReq
				return Result{
					Ok:        true,
					OutputDir: "/tmp/out",
				}, nil
			},
		},
		ResolvePassword: func(string, bool, io.Reader, io.Writer, bool) (string, error) {
			return "pw", nil
		},
	})
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	err := cmd.Execute()
	return request, stdout, stderr, err
}

func jsonContains(src string, token string) bool {
	return strings.Contains(src, token)
}

func nonEmptyLines(src string) []string {
	raw := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
