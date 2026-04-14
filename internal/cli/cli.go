package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type passwordOptions struct {
	flagPassword string
	fromStdin    bool
}

type runFlags struct {
	outputAll   bool
	output      string
	outDir      string
	timeoutMS   int
	settleMS    int
	debug       bool
	json        bool
	progress    string
	browser     string
	browserPath string
}

func NewRootCommand(cfg Config) *cobra.Command {
	cfg = cfg.withDefaults()

	root := &cobra.Command{
		Use:     "get-feishu-docs <url>",
		Short:   "Export Feishu docs with browser automation",
		Long:    "输入文档链接并输出标准化产物。默认导出 HTML，支持 PNG/PDF/Markdown 扩展格式。",
		Args:    rootArgsValidator,
		RunE:    captureRunE(cfg),
		Aliases: []string{},
	}
	if cfg.OutWriter == nil {
		cfg.OutWriter = io.Discard
	}
	if cfg.ErrWriter == nil {
		cfg.ErrWriter = io.Discard
	}
	if cfg.InReader == nil {
		cfg.InReader = strings.NewReader("")
	}
	root.SetOut(cfg.OutWriter)
	root.SetErr(cfg.ErrWriter)
	root.SetIn(cfg.InReader)
	root.SilenceUsage = true
	root.SilenceErrors = true

	attachCommonFlags(root)
	root.Example = `  # 最短流程
  get-feishu-docs https://example.feishu.cn/doc/docid

  # 按密码抓取
  get-feishu-docs --password 'Pa55w0rd' https://example.feishu.cn/doc/docid

  # 全量导出
  get-feishu-docs --output-all --out-dir ./output https://example.feishu.cn/doc/docid

  # Agent 使用
  get-feishu-docs --json --progress=jsonl https://example.feishu.cn/doc/docid

  # 环境检查
  get-feishu-docs doctor

  # 预装浏览器
  get-feishu-docs browser install`

	root.AddCommand(newCaptureCommand(cfg))
	root.AddCommand(newDoctorCommand(cfg))
	root.AddCommand(newBrowserCommand(cfg))

	return root
}

func attachCommonFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("password", "", "doc password when prompted page appears")
	cmd.PersistentFlags().Bool("password-stdin", false, "read password from stdin")
	cmd.PersistentFlags().String("out-dir", "./output", "output folder")
	cmd.PersistentFlags().String("output", "html", "comma-separated outputs: html,png,pdf,md")
	cmd.PersistentFlags().Bool("output-all", false, "export html,png,pdf,md")
	cmd.PersistentFlags().Bool("json", false, "print final JSON result to stdout")
	cmd.PersistentFlags().String("progress", ProgressText, "progress format: text|jsonl")
	cmd.PersistentFlags().String("browser", string(BrowserAuto), "browser mode: auto|system|managed")
	cmd.PersistentFlags().String("browser-path", "", "explicit browser executable path")
	cmd.PersistentFlags().Int("timeout-ms", 120000, "document load timeout in ms")
	cmd.PersistentFlags().Int("settle-ms", 500, "post-load settle time in ms")
	cmd.PersistentFlags().Bool("debug", false, "keep debug artifacts")
}

func newCaptureCommand(cfg Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capture <url>",
		Short: "Capture one Feishu document",
		Args:  captureArgsValidator,
		RunE:  captureRunE(cfg),
	}
	return cmd
}

func newDoctorCommand(cfg Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check runtime and browser readiness",
		RunE: func(c *cobra.Command, args []string) error {
			result, err := cfg.Handlers.Doctor()
			if err != nil {
				return writeErrorJSON(c, err)
			}
			return writeJSON(c.OutOrStdout(), result)
		},
	}
	return cmd
}

func newBrowserCommand(cfg Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "browser",
		Short: "Browser dependency helpers",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Print managed/system browser status",
		RunE: func(c *cobra.Command, args []string) error {
			result, err := cfg.Handlers.BrowserStatus()
			if err != nil {
				return writeErrorJSON(c, err)
			}
			return writeJSON(c.OutOrStdout(), result)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install managed Chromium",
		RunE: func(c *cobra.Command, args []string) error {
			result, err := cfg.Handlers.BrowserInstall()
			if err != nil {
				return writeErrorJSON(c, err)
			}
			return writeJSON(c.OutOrStdout(), result)
		},
	})
	return cmd
}

func rootArgsValidator(cmd *cobra.Command, args []string) error {
	if cmd.Name() == "get-feishu-docs" && len(args) == 0 {
		return cmd.Help()
	}
	if cmd.Name() != "get-feishu-docs" && len(args) == 0 {
		return nil
	}
	return captureArgsValidator(cmd, args)
}

func captureArgsValidator(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("exactly one document URL is required")
	}
	return nil
}

func captureRunE(cfg Config) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		flags, err := collectRunFlags(cmd)
		if err != nil {
			return CLIError{Code: ErrorCodeUsage, Message: err.Error()}
		}
		passwordOpts, err := collectPasswordOptions(cmd)
		if err != nil {
			return CLIError{Code: ErrorCodeUsage, Message: err.Error()}
		}

		outputs, err := cfg.ParseOutputFormats(flags.output, flags.outputAll)
		if err != nil {
			return CLIError{Code: ErrorCodeUsage, Message: err.Error()}
		}
		browserMode, err := ValidateBrowserMode(flags.browser)
		if err != nil {
			return CLIError{Code: ErrorCodeUsage, Message: err.Error()}
		}
		if err := ValidateProgressMode(flags.progress); err != nil {
			return CLIError{Code: ErrorCodeUsage, Message: err.Error()}
		}

		password, err := cfg.ResolvePassword(
			passwordOpts.flagPassword,
			passwordOpts.fromStdin,
			cmd.InOrStdin(),
			cmd.ErrOrStderr(),
			isTTY(cmd.InOrStdin()),
		)
		if err != nil {
			return CLIError{Code: ErrorCodePasswordRequired, Message: err.Error()}
		}

		progress := makeProgressSink(flags.progress, cmd.ErrOrStderr())
		progress(ProgressEvent{
			Type:      "stage",
			Stage:     "init",
			Message:   "command_ready",
			Timestamp: time.Now().Format(time.RFC3339Nano),
		})

		result, err := cfg.Handlers.Capture(CaptureRequest{
			URL:         args[0],
			Password:    password,
			OutDir:      flags.outDir,
			Outputs:     outputs,
			BrowserMode: browserMode,
			BrowserPath: flags.browserPath,
			TimeoutMS:   flags.timeoutMS,
			SettleMS:    flags.settleMS,
			Debug:       flags.debug,
		}, progress)
		if err != nil {
			progress(ProgressEvent{
				Type:      "failure",
				Stage:     "capture",
				Message:   err.Error(),
				Timestamp: time.Now().Format(time.RFC3339Nano),
			})
			return emitCaptureError(cmd, result, err, flags.json)
		}

		progress(ProgressEvent{
			Type:      "stage",
			Stage:     "done",
			Message:   "capture_complete",
			Timestamp: time.Now().Format(time.RFC3339Nano),
		})
		if flags.json {
			return writeJSON(cmd.OutOrStdout(), result)
		}
		emitSummary(cmd.OutOrStdout(), result)
		return nil
	}
}

func collectRunFlags(cmd *cobra.Command) (runFlags, error) {
	outputAll, err := getBoolFlag(cmd, "output-all")
	if err != nil {
		return runFlags{}, err
	}
	output, err := getStringFlag(cmd, "output")
	if err != nil {
		return runFlags{}, err
	}
	outDir, err := getStringFlag(cmd, "out-dir")
	if err != nil {
		return runFlags{}, err
	}
	timeoutMS, err := getIntFlag(cmd, "timeout-ms")
	if err != nil {
		return runFlags{}, err
	}
	settleMS, err := getIntFlag(cmd, "settle-ms")
	if err != nil {
		return runFlags{}, err
	}
	debug, err := getBoolFlag(cmd, "debug")
	if err != nil {
		return runFlags{}, err
	}
	jsonMode, err := getBoolFlag(cmd, "json")
	if err != nil {
		return runFlags{}, err
	}
	progress, err := getStringFlag(cmd, "progress")
	if err != nil {
		return runFlags{}, err
	}
	browser, err := getStringFlag(cmd, "browser")
	if err != nil {
		return runFlags{}, err
	}
	browserPath, err := getStringFlag(cmd, "browser-path")
	if err != nil {
		return runFlags{}, err
	}
	return runFlags{
		outputAll:   outputAll,
		output:      strings.TrimSpace(output),
		outDir:      strings.TrimSpace(outDir),
		timeoutMS:   timeoutMS,
		settleMS:    settleMS,
		debug:       debug,
		json:        jsonMode,
		progress:    progress,
		browser:     browser,
		browserPath: strings.TrimSpace(browserPath),
	}, nil
}

func collectPasswordOptions(cmd *cobra.Command) (passwordOptions, error) {
	password, err := getStringFlag(cmd, "password")
	if err != nil {
		return passwordOptions{}, err
	}
	passwordStdin, err := getBoolFlag(cmd, "password-stdin")
	if err != nil {
		return passwordOptions{}, err
	}
	return passwordOptions{
		flagPassword: strings.TrimSpace(password),
		fromStdin:    passwordStdin,
	}, nil
}

func getStringFlag(cmd *cobra.Command, name string) (string, error) {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		return cmd.Flags().GetString(name)
	}
	if flag := cmd.InheritedFlags().Lookup(name); flag != nil {
		return cmd.InheritedFlags().GetString(name)
	}
	return "", fmt.Errorf("missing flag: %s", name)
}

func getBoolFlag(cmd *cobra.Command, name string) (bool, error) {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		return cmd.Flags().GetBool(name)
	}
	if flag := cmd.InheritedFlags().Lookup(name); flag != nil {
		return cmd.InheritedFlags().GetBool(name)
	}
	return false, fmt.Errorf("missing flag: %s", name)
}

func getIntFlag(cmd *cobra.Command, name string) (int, error) {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		return cmd.Flags().GetInt(name)
	}
	if flag := cmd.InheritedFlags().Lookup(name); flag != nil {
		return cmd.InheritedFlags().GetInt(name)
	}
	return 0, fmt.Errorf("missing flag: %s", name)
}

func ResolvePassword(flagValue string, fromStdin bool, in io.Reader, out io.Writer, tty bool) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if fromStdin {
		line, err := readLine(in)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	if tty {
		return promptPassword(in, out)
	}
	return "", nil
}

func readLine(r io.Reader) (string, error) {
	scanner := bufio.NewReader(r)
	line, err := scanner.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimRight(line, "\n"), nil
}

func promptPassword(in io.Reader, out io.Writer) (string, error) {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(out, "Password: ")
		return readLine(in)
	}

	fmt.Fprint(out, "Password: ")
	password, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprint(out, "\n")
	return string(password), err
}

func isTTY(in io.Reader) bool {
	file, ok := in.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func makeProgressSink(mode string, out io.Writer) func(ProgressEvent) {
	if mode == ProgressJSON {
		enc := json.NewEncoder(out)
		return func(event ProgressEvent) {
			if event.Timestamp == "" {
				event.Timestamp = time.Now().Format(time.RFC3339Nano)
			}
			_ = enc.Encode(event)
		}
	}
	return func(event ProgressEvent) {
		if event.Timestamp == "" {
			event.Timestamp = time.Now().Format(time.RFC3339Nano)
		}
		fmt.Fprintf(out, "[get-feishu-docs] %s\n", event.Message)
	}
}

func emitCaptureError(cmd *cobra.Command, result Result, err error, jsonMode bool) error {
	clErr := asCLIError(err)
	if result.Error == nil {
		result.Error = &ErrorResult{Code: string(clErr.Code), Message: clErr.Message}
	}
	if result.Ok {
		result.Ok = false
	}
	if jsonMode {
		_ = writeJSON(cmd.OutOrStdout(), result)
		return clErr
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "capture failed: %s (%s)\n", clErr.Message, clErr.Code)
	return clErr
}

func emitSummary(out io.Writer, result Result) {
	if result.Ok {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "抓取完成")
		fmt.Fprintf(out, "标题: %s\n", result.Title)
		fmt.Fprintf(out, "输出目录: %s\n", result.OutputDir)
		fmt.Fprintln(out)
		fmt.Fprintln(out, "主要文件:")
		for _, file := range result.Files {
			fmt.Fprintf(out, "- %s: %s\n", strings.ToUpper(file.Kind), file.Path)
		}
		if len(result.Stats) > 0 {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "统计:")
			if value, ok := result.Stats["blocks"]; ok {
				fmt.Fprintf(out, "- %d 个区块\n", value)
			}
			if value, ok := result.Stats["embeddedImages"]; ok {
				fmt.Fprintf(out, "- %d 张原图资源\n", value)
			}
			if value, ok := result.Stats["visualFallbackBlocks"]; ok {
				fmt.Fprintf(out, "- %d 个截图兜底块\n", value)
			}
		}
		return
	}
	if result.Error != nil {
		fmt.Fprintf(out, "failed: %s (%s)\n", result.Error.Message, result.Error.Code)
	}
}

func writeErrorJSON(cmd *cobra.Command, err error) error {
	clErr := asCLIError(err)
	payload := struct {
		Ok    bool         `json:"ok"`
		Error *ErrorResult `json:"error"`
	}{
		Ok: false,
		Error: &ErrorResult{
			Code:    string(clErr.Code),
			Message: clErr.Message,
		},
	}
	_ = writeJSON(cmd.OutOrStdout(), payload)
	return clErr
}

func writeJSON(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	return enc.Encode(payload)
}

func asCLIError(err error) CLIError {
	if ce, ok := err.(CLIError); ok {
		return ce
	}
	return CLIError{
		Code:    ErrorCodeCaptureFailed,
		Message: err.Error(),
	}
}
