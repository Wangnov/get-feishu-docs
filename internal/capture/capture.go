package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	ibrowser "github.com/wangnov/get-feishu-docs/internal/browser"
	"github.com/wangnov/get-feishu-docs/internal/contracts"
)

var passwordButtonPattern = []string{
	"确定", "确认", "继续访问", "提交", "进入", "下一步", "完成", "解锁", "打开",
	"submit", "confirm", "continue", "enter", "unlock",
}

var passwordFieldPattern = []string{"密码", "password", "passcode", "访问码", "口令"}

var documentReadySelectors = []string{
	".page-main-item.editor",
	".page-main .editor-container",
	".docx-page-block",
	".page-block-header",
}

const passwordPromptTimeout = 8 * time.Second

func Run(ctx context.Context, req contracts.CaptureRequest, emit func(contracts.ProgressEvent)) (contracts.Result, error) {
	if strings.TrimSpace(req.URL) == "" {
		return failureResult(contracts.ErrorCodeUsage, "a Feishu document URL is required"), contracts.NewCLIError(contracts.ErrorCodeUsage, "a Feishu document URL is required", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if req.TimeoutMS <= 0 {
		req.TimeoutMS = 45000
	}
	if req.SettleMS < 0 {
		req.SettleMS = 0
	}
	if len(req.Outputs) == 0 {
		req.Outputs = []string{"html"}
	}

	progress := newProgressEmitter(emit)
	progress("startup", "1/6 正在启动浏览器。", nil)

	session, err := ibrowser.StartBrowser(ctx, ibrowser.StartOptions{
		Mode:        req.BrowserMode,
		BrowserPath: req.BrowserPath,
		Headless:    true,
	})
	if err != nil {
		return failureResult(contracts.ErrorCodeBrowserUnavailable, err.Error()), err
	}
	defer session.Close()

	page, err := session.Browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return failureResult(contracts.ErrorCodeCaptureFailed, "failed to create browser page"), contracts.NewCLIError(contracts.ErrorCodeCaptureFailed, "failed to create browser page", err)
	}
	defer page.Close()

	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:             1440,
		Height:            1280,
		DeviceScaleFactor: 1,
		Mobile:            false,
	}); err != nil {
		return failureResult(contracts.ErrorCodeCaptureFailed, "failed to set viewport"), contracts.NewCLIError(contracts.ErrorCodeCaptureFailed, "failed to set viewport", err)
	}

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	progress("open", "2/6 正在打开飞书文档。", nil)
	if err := navigateAndWait(page.Timeout(timeout), req.URL); err != nil {
		return failureResult(contracts.ErrorCodeDocumentTimeout, "failed to open document"), contracts.NewCLIError(contracts.ErrorCodeDocumentTimeout, "failed to open document", err)
	}

	usedPassword := false
	if req.Password != "" {
		progress("auth", "2/6 检测到已提供密码，正在尝试进入文档。", nil)
		var unlocked bool
		unlocked, err = maybeUnlockWithPassword(page.Timeout(passwordPromptTimeout), req.Password)
		if err != nil {
			return failureResult(contracts.ErrorCodePasswordRequired, "failed to submit password"), contracts.NewCLIError(contracts.ErrorCodePasswordRequired, "failed to submit password", err)
		}
		usedPassword = unlocked
		if unlocked {
			_ = page.WaitLoad()
			_ = page.WaitIdle(2 * time.Second)
		}
	}

	readySelector, err := waitForDocument(page.Timeout(timeout), timeout)
	if err != nil {
		return failureResult(contracts.ErrorCodeDocumentTimeout, "timed out waiting for document body"), contracts.NewCLIError(contracts.ErrorCodeDocumentTimeout, "timed out waiting for document body", err)
	}
	progress("open", fmt.Sprintf("2/6 正文已渲染，命中选择器：%s", readySelector), nil)
	if req.SettleMS > 0 {
		time.Sleep(time.Duration(req.SettleMS) * time.Millisecond)
	}

	title, _ := evalString(page, `() => document.title || ""`)
	finalURL := req.URL
	if info, infoErr := page.Info(); infoErr == nil && info != nil && info.URL != "" {
		finalURL = info.URL
	}
	html, err := page.HTML()
	if err != nil {
		return failureResult(contracts.ErrorCodeCaptureFailed, "failed to read page html"), contracts.NewCLIError(contracts.ErrorCodeCaptureFailed, "failed to read page html", err)
	}
	mhtml, err := captureMHTML(page)
	if err != nil {
		return failureResult(contracts.ErrorCodeCaptureFailed, "failed to capture page snapshot"), contracts.NewCLIError(contracts.ErrorCodeCaptureFailed, "failed to capture page snapshot", err)
	}

	metadata := CaptureMetadata{
		URL:                   req.URL,
		FinalURL:              finalURL,
		Title:                 title,
		CapturedAt:            time.Now().Format(time.RFC3339),
		ReadySelector:         readySelector,
		UsedPassword:          usedPassword,
		BodyOnlyHideSelectors: append([]string(nil), bodyOnlyHideSelectors...),
	}

	layout, err := createOutputLayout(req.OutDir, metadata.Title, req.Debug)
	if err != nil {
		return failureResult(contracts.ErrorCodeWriteFailed, "failed to prepare output layout"), contracts.NewCLIError(contracts.ErrorCodeWriteFailed, "failed to prepare output layout", err)
	}
	defer layout.cleanup()

	progress("snapshot", fmt.Sprintf("输出目录已准备好：%s", layout.rootDir), nil)
	progress("snapshot", "3/6 正在保存中间快照。", nil)
	if err := os.WriteFile(layout.rawPageHtmlPath, []byte(html), 0o644); err != nil {
		return failureResult(contracts.ErrorCodeWriteFailed, "failed to write raw page html"), contracts.NewCLIError(contracts.ErrorCodeWriteFailed, "failed to write raw page html", err)
	}
	if err := os.WriteFile(layout.rawPageMhtmlPath, []byte(mhtml), 0o644); err != nil {
		return failureResult(contracts.ErrorCodeWriteFailed, "failed to write raw page mhtml"), contracts.NewCLIError(contracts.ErrorCodeWriteFailed, "failed to write raw page mhtml", err)
	}
	if err := os.WriteFile(layout.metadataPath, mustJSON(metadata), 0o644); err != nil {
		return failureResult(contracts.ErrorCodeWriteFailed, "failed to write metadata"), contracts.NewCLIError(contracts.ErrorCodeWriteFailed, "failed to write metadata", err)
	}
	page.MustScreenshotFullPage(layout.rawPagePngPath)

	expandedFolds, err := expandAllFoldedBlocks(page)
	if err != nil {
		return failureResult(contracts.ErrorCodeCaptureFailed, "failed to expand folded blocks"), contracts.NewCLIError(contracts.ErrorCodeCaptureFailed, "failed to expand folded blocks", err)
	}
	if expandedFolds > 0 {
		progress("prune", fmt.Sprintf("4/6 已展开 %d 个折叠区块，正在整理正文快照。", expandedFolds), map[string]int{
			"expandedFolds": expandedFolds,
		})
	} else {
		progress("prune", "4/6 正在整理正文快照。", nil)
	}
	if err := applyBodyOnlyPrune(page); err != nil {
		return failureResult(contracts.ErrorCodeCaptureFailed, "failed to prune body-only content"), contracts.NewCLIError(contracts.ErrorCodeCaptureFailed, "failed to prune body-only content", err)
	}
	if req.SettleMS > 0 {
		time.Sleep(time.Duration(min(req.SettleMS, 1000)) * time.Millisecond)
	}

	bodyHTML, err := page.HTML()
	if err != nil {
		return failureResult(contracts.ErrorCodeCaptureFailed, "failed to read pruned body html"), contracts.NewCLIError(contracts.ErrorCodeCaptureFailed, "failed to read pruned body html", err)
	}
	bodyMHTML, err := captureMHTML(page)
	if err != nil {
		return failureResult(contracts.ErrorCodeCaptureFailed, "failed to capture pruned body snapshot"), contracts.NewCLIError(contracts.ErrorCodeCaptureFailed, "failed to capture pruned body snapshot", err)
	}
	if err := os.WriteFile(layout.bodyHtmlPath, []byte(bodyHTML), 0o644); err != nil {
		return failureResult(contracts.ErrorCodeWriteFailed, "failed to write body html"), contracts.NewCLIError(contracts.ErrorCodeWriteFailed, "failed to write body html", err)
	}
	if err := os.WriteFile(layout.bodySnapshotMhtmlPath, []byte(bodyMHTML), 0o644); err != nil {
		return failureResult(contracts.ErrorCodeWriteFailed, "failed to write body snapshot"), contracts.NewCLIError(contracts.ErrorCodeWriteFailed, "failed to write body snapshot", err)
	}
	page.MustScreenshotFullPage(layout.bodyPreviewPngPath)

	progress("extract", "5/6 正在采集正文结构与图片资源。", nil)
	structured, err := extractStructuredDocument(page, layout, progress)
	if err != nil {
		return failureResult(contracts.ErrorCodeSelectorDrift, "failed to extract structured content"), contracts.NewCLIError(contracts.ErrorCodeSelectorDrift, "failed to extract structured content", err)
	}

	result := contracts.Result{
		Ok:              true,
		Title:           stripFeishuTitleSuffix(metadata.Title),
		TitleRaw:        metadata.Title,
		OutputDir:       layout.rootDir,
		SelectedOutputs: append([]string(nil), req.Outputs...),
		Stats: map[string]int{
			"blocks":               structured.TotalBlocks,
			"embeddedImages":       structured.CapturedAssetCount,
			"visualFallbackBlocks": structured.CapturedScreenshotCount,
			"maxVisibleBlocks":     structured.MaxVisibleBlocks,
		},
	}
	if layout.reportPath != "" {
		result.Debug = map[string]any{
			"dir":               coalesce(layout.debugDir, layout.workingRoot),
			"rawDir":            layout.rawDir,
			"bodyDir":           layout.bodyDir,
			"dataDir":           layout.dataDir,
			"rawPageMhtml":      layout.rawPageMhtmlPath,
			"bodySnapshotMhtml": layout.bodySnapshotMhtmlPath,
			"bodyPreviewPng":    layout.bodyPreviewPngPath,
		}
	}
	if layout.reportPath != "" {
		if err := os.WriteFile(layout.reportPath, mustJSON(result), 0o644); err == nil {
			result.Files = append(result.Files, contracts.FileArtifact{Kind: "report", Path: layout.reportPath})
		}
	}

	progress("render", "6/6 正在生成基础 HTML。", nil)
	if err := buildReplicaHTML(layout); err != nil {
		return failureResult(contracts.ErrorCodeCaptureFailed, "failed to build replica html"), contracts.NewCLIError(contracts.ErrorCodeCaptureFailed, "failed to build replica html", err)
	}
	files, err := exportRequestedOutputs(session.Browser, page, layout, req.Outputs, progress)
	if err != nil {
		return failureResult(contracts.ErrorCodeWriteFailed, "failed to export requested outputs"), contracts.NewCLIError(contracts.ErrorCodeWriteFailed, "failed to export requested outputs", err)
	}
	result.Files = append(result.Files, files...)
	if err := writeManifest(layout, result, metadata); err == nil {
		result.Files = append(result.Files, contracts.FileArtifact{Kind: "manifest", Path: layout.manifestPath})
	}
	if layout.reportPath != "" {
		_ = os.WriteFile(layout.reportPath, mustJSON(result), 0o644)
	}

	progress("done", "已完成，最终结果已经写入输出目录。", result.Stats)
	return result, nil
}

func writeManifest(layout *outputLayout, result contracts.Result, metadata CaptureMetadata) error {
	manifest := map[string]any{
		"ok":              result.Ok,
		"title":           result.Title,
		"titleRaw":        result.TitleRaw,
		"outputDir":       result.OutputDir,
		"selectedOutputs": result.SelectedOutputs,
		"files":           result.Files,
		"stats":           result.Stats,
		"source": map[string]any{
			"url":           metadata.URL,
			"finalUrl":      metadata.FinalURL,
			"capturedAt":    metadata.CapturedAt,
			"readySelector": metadata.ReadySelector,
			"usedPassword":  metadata.UsedPassword,
		},
		"assets": manifestAssets(layout),
	}
	return os.WriteFile(layout.manifestPath, mustJSON(manifest), 0o644)
}

func manifestAssets(layout *outputLayout) []map[string]any {
	entries, err := os.ReadDir(layout.replicaAssetsDir)
	if err != nil {
		return nil
	}

	assets := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		relativePath := filepath.ToSlash(filepath.Join(filepath.Base(layout.replicaAssetsDir), entry.Name()))
		assets = append(assets, map[string]any{
			"name":         entry.Name(),
			"relativePath": relativePath,
			"path":         filepath.Join(layout.replicaAssetsDir, entry.Name()),
			"sizeBytes":    info.Size(),
			"kind":         manifestAssetKind(entry.Name()),
		})
	}
	sort.Slice(assets, func(i, j int) bool {
		return assets[i]["relativePath"].(string) < assets[j]["relativePath"].(string)
	})
	return assets
}

func manifestAssetKind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".webm", ".mkv", ".m4v":
		return "video"
	default:
		return "image"
	}
}

func navigateAndWait(page *rod.Page, targetURL string) error {
	if err := page.Navigate(targetURL); err != nil {
		return err
	}
	if err := page.WaitLoad(); err != nil {
		return err
	}
	_ = page.WaitIdle(2 * time.Second)
	time.Sleep(700 * time.Millisecond)
	return nil
}

func expandAllFoldedBlocks(page *rod.Page) (int, error) {
	totalExpanded := 0
	for attempt := 0; attempt < 8; attempt++ {
		obj, err := page.Evaluate(rod.Eval(`() => {
			const wrappers = Array.from(document.querySelectorAll('.fold-wrapper.fold-folded.can-fold'))
				.filter((node) => !node.classList.contains('fold-handler-wrapper'));
			let clicked = 0;
			for (const wrapper of wrappers) {
				const target =
					wrapper.querySelector('.fold-handler') ||
					wrapper.querySelector('.fold-handler-wrapper') ||
					wrapper;
				if (!(target instanceof HTMLElement)) {
					continue;
				}
				target.click();
				clicked += 1;
			}
			return clicked;
		}`))
		if err != nil {
			return totalExpanded, err
		}
		clicked := obj.Value.Int()
		if clicked <= 0 {
			break
		}
		totalExpanded += int(clicked)
		time.Sleep(450 * time.Millisecond)
	}
	return totalExpanded, nil
}

func maybeUnlockWithPassword(page *rod.Page, password string) (bool, error) {
	deadline := time.Now().Add(passwordPromptTimeout)
	for time.Now().Before(deadline) {
		input, err := findPasswordInput(page)
		if err != nil {
			return false, err
		}
		if input == nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}

		if err := input.ScrollIntoView(); err != nil {
			return false, err
		}
		if err := input.Input(password); err != nil {
			return false, err
		}

		button, err := findPasswordButton(page)
		if err != nil {
			return false, err
		}
		if button != nil {
			if err := button.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return false, err
			}
		} else {
			if _, err := input.Eval(`() => {
				const form = this.form;
				if (form && typeof form.requestSubmit === "function") {
					form.requestSubmit();
					return true;
				}
				if (form && typeof form.submit === "function") {
					form.submit();
					return true;
				}
				this.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
				this.dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", bubbles: true }));
				return true;
			}`); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	return false, nil
}

func waitForDocument(page *rod.Page, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := page.Eval(`(selectors) => {
			const visible = (node) => !!(node && (node.offsetWidth || node.offsetHeight || node.getClientRects().length));
			for (const selector of selectors) {
				const node = document.querySelector(selector);
				if (visible(node)) return selector;
			}
			return "";
		}`, documentReadySelectors)
		if err == nil {
			selector := strings.TrimSpace(res.Value.Str())
			if selector != "" {
				return selector, nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "", errors.New("timed out waiting for Feishu document body to render")
}

func findPasswordInput(page *rod.Page) (*rod.Element, error) {
	elements, err := page.Elements("input")
	if err != nil {
		return nil, err
	}
	for _, element := range elements {
		visible, _ := element.Visible()
		if !visible {
			continue
		}
		if matchesPasswordField(element) {
			return element, nil
		}
	}
	return nil, nil
}

func matchesPasswordField(element *rod.Element) bool {
	attrs := []string{"type", "autocomplete", "placeholder", "aria-label", "name"}
	values := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		value, _ := element.Attribute(attr)
		if value != nil {
			values = append(values, strings.ToLower(strings.TrimSpace(*value)))
		}
	}
	for _, value := range values {
		if value == "password" || value == "current-password" {
			return true
		}
		for _, token := range passwordFieldPattern {
			if strings.Contains(value, strings.ToLower(token)) {
				return true
			}
		}
	}
	return false
}

func findPasswordButton(page *rod.Page) (*rod.Element, error) {
	elements, err := page.Elements("button, input[type='submit'], input[type='button']")
	if err != nil {
		return nil, err
	}
	for _, element := range elements {
		visible, _ := element.Visible()
		if !visible {
			continue
		}
		label := ""
		if text, err := element.Text(); err == nil {
			label = text
		}
		if strings.TrimSpace(label) == "" {
			if value, _ := element.Attribute("value"); value != nil {
				label = *value
			}
		}
		label = strings.ToLower(strings.TrimSpace(label))
		for _, token := range passwordButtonPattern {
			if strings.Contains(label, strings.ToLower(token)) {
				return element, nil
			}
		}
	}
	return nil, nil
}

func captureMHTML(page *rod.Page) (string, error) {
	snapshot, err := proto.PageCaptureSnapshot{
		Format: proto.PageCaptureSnapshotFormatMhtml,
	}.Call(page)
	if err != nil {
		return "", err
	}
	return snapshot.Data, nil
}

func evalString(page *rod.Page, js string, args ...any) (string, error) {
	obj, err := page.Eval(js, args...)
	if err != nil {
		return "", err
	}
	return obj.Value.Str(), nil
}

func newProgressEmitter(emit func(contracts.ProgressEvent)) func(string, string, map[string]int) {
	if emit == nil {
		return func(string, string, map[string]int) {}
	}
	return func(stage, message string, stats map[string]int) {
		emit(contracts.ProgressEvent{
			Type:      "stage",
			Stage:     stage,
			Message:   message,
			Stats:     stats,
			Timestamp: time.Now().Format(time.RFC3339Nano),
		})
	}
}

func failureResult(code contracts.ErrorCode, message string) contracts.Result {
	return contracts.Result{
		Ok: false,
		Error: &contracts.ErrorResult{
			Code:    string(code),
			Message: message,
		},
	}
}

func mustJSON(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return append(data, '\n')
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func coalesce(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}
