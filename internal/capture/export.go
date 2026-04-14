package capture

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/PuerkitoBio/goquery"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"github.com/wangnov/get-feishu-docs/internal/contracts"
)

func exportRequestedOutputs(browser *rod.Browser, sourcePage *rod.Page, layout *outputLayout, formats []string, progress func(string, string, map[string]int)) ([]contracts.FileArtifact, error) {
	produced := []contracts.FileArtifact{}
	if err := localizeReplicaVideos(layout, sourcePage); err != nil {
		return nil, err
	}
	if err := syncReplicaAssets(layout); err != nil {
		return nil, err
	}
	if hasFiles(layout.replicaAssetsDir) {
		produced = append(produced, contracts.FileArtifact{Kind: "assets", Path: layout.replicaAssetsDir})
	}
	if containsString(formats, "html") {
		data, err := os.ReadFile(layout.workingReplicaHtmlPath)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(layout.replicaHtmlPath, data, 0o644); err != nil {
			return nil, err
		}
		produced = append(produced, contracts.FileArtifact{Kind: "html", Path: layout.replicaHtmlPath})
	}
	if containsString(formats, "png") {
		progress("export", "6/6 正在生成整页 PNG。", nil)
		if err := exportPNG(browser, layout); err != nil {
			return nil, err
		}
		produced = append(produced, contracts.FileArtifact{Kind: "png", Path: layout.replicaPngPath})
	}
	if containsString(formats, "pdf") {
		progress("export", "6/6 正在生成 PDF。", nil)
		if err := exportPDF(browser, layout); err != nil {
			return nil, err
		}
		produced = append(produced, contracts.FileArtifact{Kind: "pdf", Path: layout.replicaPdfPath})
	}
	if containsString(formats, "md") {
		progress("export", "6/6 正在生成 Markdown。", nil)
		markdownFiles, err := exportMarkdown(layout)
		if err != nil {
			return nil, err
		}
		produced = append(produced, markdownFiles...)
	}
	return produced, nil
}

func localizeReplicaVideos(layout *outputLayout, sourcePage *rod.Page) error {
	html, err := os.ReadFile(layout.workingReplicaHtmlPath)
	if err != nil {
		return err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		return err
	}

	assetsDirName := filepath.Base(layout.replicaAssetsDir)
	downloadCache := map[string]string{}
	videoIndex := 1
	referer := ""
	if sourcePage != nil {
		if info, infoErr := sourcePage.Info(); infoErr == nil && info != nil {
			referer = info.URL
		}
	}

	doc.Find(".restored-file-block").Each(func(_ int, block *goquery.Selection) {
		video := block.Find(".restored-file-video-player").First()
		if video.Length() == 0 {
			return
		}
		source := video.Find("source").First()
		sourceURL, hasSource := source.Attr("src")
		if !hasSource || !isRemoteURL(sourceURL) {
			if videoSrc, ok := video.Attr("src"); ok && isRemoteURL(videoSrc) {
				sourceURL = videoSrc
				hasSource = true
			}
		}
		if !hasSource || !isRemoteURL(sourceURL) {
			return
		}

		localRelPath, ok := downloadCache[sourceURL]
		if !ok {
			title := strings.TrimSpace(block.Find(".restored-file-name").First().Text())
			localRelPath, err = downloadVideoAsset(layout.workingReplicaAssetsDir, assetsDirName, sourcePage, referer, sourceURL, title, videoIndex)
			if err != nil {
				return
			}
			downloadCache[sourceURL] = localRelPath
			videoIndex++
		}

		if source.Length() > 0 {
			source.SetAttr("src", localRelPath)
			video.RemoveAttr("src")
		} else {
			video.SetAttr("src", localRelPath)
		}

		if link := block.Find(".restored-file-link").First(); link.Length() > 0 {
			link.SetAttr("href", localRelPath)
			link.SetAttr("download", "")
			link.SetText("打开本地视频")
		}
	})

	rendered, err := doc.Html()
	if err != nil {
		return err
	}
	return os.WriteFile(layout.workingReplicaHtmlPath, []byte(rendered), 0o644)
}

func downloadVideoAsset(targetDir, assetsDirName string, sourcePage *rod.Page, referer, sourceURL, title string, index int) (string, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "get-feishu-docs/1.0")
	if referer != "" {
		req.Header.Set("Referer", referer)
		if refererURL, parseErr := url.Parse(referer); parseErr == nil {
			req.Header.Set("Origin", refererURL.Scheme+"://"+refererURL.Host)
		}
	}

	client := &http.Client{Timeout: 2 * time.Minute}
	if sourcePage != nil {
		if jar, jarErr := cookiejar.New(nil); jarErr == nil {
			if parsedURL, parseErr := url.Parse(sourceURL); parseErr == nil {
				cookies := make([]*http.Cookie, 0, 8)
				for _, cookie := range sourcePage.MustCookies(sourceURL) {
					if cookie == nil {
						continue
					}
					httpCookie := &http.Cookie{
						Name:     cookie.Name,
						Value:    cookie.Value,
						Path:     cookie.Path,
						Domain:   cookie.Domain,
						Secure:   cookie.Secure,
						HttpOnly: cookie.HTTPOnly,
					}
					if cookie.Expires != 0 {
						httpCookie.Expires = time.Unix(int64(cookie.Expires), 0)
					}
					cookies = append(cookies, httpCookie)
				}
				jar.SetCookies(parsedURL, cookies)
				client.Jar = jar
			}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("video download failed: %s", resp.Status)
	}

	extension := pickVideoExtension(resp.Header.Get("Content-Type"), title, sourceURL)
	baseName := sanitizeVideoBaseName(title, index)
	filename := uniqueReplicaFilename(targetDir, baseName, extension)
	absPath := filepath.Join(targetDir, filename)

	file, err := os.Create(absPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		_ = os.Remove(absPath)
		return "", err
	}
	return filepath.ToSlash(filepath.Join(assetsDirName, filename)), nil
}

func pickVideoExtension(contentType, title, sourceURL string) string {
	if value := extensionFromVideoContentType(contentType); value != "" {
		return value
	}
	if value := extensionFromPath(title); value != "" {
		return value
	}
	if value := extensionFromPath(sourceURL); value != "" {
		return value
	}
	return ".mp4"
}

func extensionFromVideoContentType(contentType string) string {
	value := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch value {
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	case "video/x-matroska":
		return ".mkv"
	}
	return ""
}

func extensionFromPath(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err == nil && parsed.Path != "" {
		value = parsed.Path
	}
	ext := strings.ToLower(filepath.Ext(value))
	switch ext {
	case ".mp4", ".mov", ".webm", ".mkv", ".m4v":
		return ext
	}
	return ""
}

func sanitizeVideoBaseName(title string, index int) string {
	trimmed := strings.TrimSpace(title)
	if ext := filepath.Ext(trimmed); ext != "" {
		trimmed = strings.TrimSuffix(trimmed, ext)
	}
	base := sanitizePathSegment(trimmed)
	if base == "feishu-doc" {
		base = fmt.Sprintf("video-%03d", index)
	}
	return base
}

func uniqueReplicaFilename(targetDir, baseName, extension string) string {
	filename := baseName + extension
	if _, err := os.Stat(filepath.Join(targetDir, filename)); os.IsNotExist(err) {
		return filename
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%02d%s", baseName, suffix, extension)
		if _, err := os.Stat(filepath.Join(targetDir, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
}

func syncReplicaAssets(layout *outputLayout) error {
	if !hasFiles(layout.workingReplicaAssetsDir) {
		return nil
	}
	if err := os.RemoveAll(layout.replicaAssetsDir); err != nil {
		return err
	}
	if err := os.MkdirAll(layout.replicaAssetsDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(layout.workingReplicaAssetsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcPath := filepath.Join(layout.workingReplicaAssetsDir, entry.Name())
		dstPath := filepath.Join(layout.replicaAssetsDir, entry.Name())
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func hasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return true
		}
	}
	return false
}

func isRemoteURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func exportPNG(browser *rod.Browser, layout *outputLayout) error {
	page, err := openReplicaPage(browser, layout)
	if err != nil {
		return err
	}
	defer page.Close()
	metrics, err := proto.PageGetLayoutMetrics{}.Call(page)
	if err != nil {
		return err
	}
	width := 980.0
	height := metrics.ContentSize.Height
	if height < 1 {
		height = 1
	}
	screenshot, err := proto.PageCaptureScreenshot{
		Format:                proto.PageCaptureScreenshotFormatPng,
		CaptureBeyondViewport: true,
		FromSurface:           true,
		Clip: &proto.PageViewport{
			X:      0,
			Y:      0,
			Width:  width,
			Height: height,
			Scale:  1,
		},
	}.Call(page)
	if err != nil {
		return err
	}
	return os.WriteFile(layout.replicaPngPath, screenshot.Data, 0o644)
}

func exportPDF(browser *rod.Browser, layout *outputLayout) error {
	page, err := openReplicaPage(browser, layout)
	if err != nil {
		return err
	}
	defer page.Close()

	margin := 12.0 / 25.4
	paperWidth := 595.92 / 72.0
	paperHeight := 842.88 / 72.0
	result, err := proto.PagePrintToPDF{
		PrintBackground: true,
		PaperWidth:      &paperWidth,
		PaperHeight:     &paperHeight,
		MarginTop:       &margin,
		MarginRight:     &margin,
		MarginBottom:    &margin,
		MarginLeft:      &margin,
	}.Call(page)
	if err != nil {
		return err
	}
	return os.WriteFile(layout.replicaPdfPath, result.Data, 0o644)
}

func exportMarkdown(layout *outputLayout) ([]contracts.FileArtifact, error) {
	if err := os.MkdirAll(layout.markdownAssetsDir, 0o755); err != nil {
		return nil, err
	}

	html, err := os.ReadFile(layout.workingReplicaHtmlPath)
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		return nil, err
	}
	article := doc.Find(".doc-page").First()
	imageIndex := 1
	assetsDirName := filepath.Base(layout.markdownAssetsDir)
	pathRewrites := map[string]string{}

	article.Find("img").Each(func(_ int, image *goquery.Selection) {
		src, _ := image.Attr("src")
		if !strings.HasPrefix(src, "data:") {
			return
		}
		payload, err := decodeEmbeddedDataURL(src)
		if err != nil {
			return
		}
		filename := fmt.Sprintf("image-%03d%s", imageIndex, extensionForMimeType(payload.MimeType))
		imageIndex++
		_ = os.WriteFile(filepath.Join(layout.markdownAssetsDir, filename), payload.Buffer, 0o644)
		relPath := filepath.ToSlash(filepath.Join(assetsDirName, filename))
		image.SetAttr("src", relPath)
		encodedRelPath := (&url.URL{Path: relPath}).EscapedPath()
		markdownRelPath := escapeMarkdownPath(relPath)
		pathRewrites[encodedRelPath] = markdownRelPath
		pathRewrites[relPath] = markdownRelPath
	})

	content, err := article.Html()
	if err != nil {
		return nil, err
	}
	markdown, err := markdownConverter().ConvertString(content)
	if err != nil {
		return nil, err
	}
	for oldValue, newValue := range pathRewrites {
		markdown = strings.ReplaceAll(markdown, oldValue, newValue)
	}
	markdown = strings.ReplaceAll(markdown, "Feishu Docs - Image", "飞书文档 - 图片")
	markdown = strings.ReplaceAll(markdown, "&gt;", ">")
	markdown = strings.ReplaceAll(markdown, "&lt;", "<")
	markdown = orderedListEscapedPattern.ReplaceAllString(markdown, "$1.")
	if err := os.WriteFile(layout.replicaMdPath, []byte(strings.TrimSpace(markdown)+"\n"), 0o644); err != nil {
		return nil, err
	}

	return []contracts.FileArtifact{
		{Kind: "md", Path: layout.replicaMdPath},
	}, nil
}

func markdownConverter() *converter.Converter {
	return converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(
			commonmark.WithHeadingStyle(commonmark.HeadingStyleATX),
			commonmark.WithBulletListMarker("-"),
			commonmark.WithCodeBlockFence("```"),
		),
		table.NewTablePlugin(
			table.WithHeaderPromotion(true),
			table.WithSpanCellBehavior(table.SpanBehaviorEmpty),
			table.WithNewlineBehavior(table.NewlineBehaviorPreserve),
			table.WithCellPaddingBehavior(table.CellPaddingBehaviorMinimal),
		),
	))
}

func openReplicaPage(browser *rod.Browser, layout *outputLayout) (*rod.Page, error) {
	fileURL := pathToFileURL(layout.workingReplicaHtmlPath)
	page, err := browser.Page(proto.TargetCreateTarget{URL: fileURL})
	if err != nil {
		return nil, err
	}
	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:             980,
		Height:            1280,
		DeviceScaleFactor: 1,
		Mobile:            false,
	}); err != nil {
		return nil, err
	}
	if err := page.WaitLoad(); err != nil {
		return nil, err
	}
	time.Sleep(400 * time.Millisecond)
	return page, nil
}

func pathToFileURL(path string) string {
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String()
}

func decodeEmbeddedDataURL(src string) (dataURLPayload, error) {
	parts := strings.SplitN(src, ",", 2)
	if len(parts) != 2 {
		return dataURLPayload{}, fmt.Errorf("invalid data url")
	}
	prefix := parts[0]
	payload := parts[1]
	mimeType := "image/png"
	if strings.HasPrefix(prefix, "data:") {
		mimeType = strings.TrimPrefix(prefix, "data:")
		mimeType = strings.TrimSuffix(mimeType, ";base64")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return dataURLPayload{}, err
	}
	return dataURLPayload{MimeType: mimeType, Buffer: data}, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func escapeMarkdownPath(value string) string {
	value = strings.ReplaceAll(value, "(", `\(`)
	value = strings.ReplaceAll(value, ")", `\)`)
	return value
}

var orderedListEscapedPattern = regexp.MustCompile(`(?m)^(\d+)\\\.$`)
