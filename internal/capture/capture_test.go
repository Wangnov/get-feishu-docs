package capture

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/wangnov/get-feishu-docs/internal/contracts"
)

func decodeBase64Fixture(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(value)
}

func TestSanitizePathSegment(t *testing.T) {
	t.Parallel()

	if got := stripFeishuTitleSuffix("示例文档 - 飞书云文档"); got != "示例文档" {
		t.Fatalf("unexpected stripped title: %q", got)
	}
	if got := sanitizePathSegment(` 示例/文档:*?  - 飞书云文档 `); got != "示例-文档" {
		t.Fatalf("unexpected sanitized path: %q", got)
	}
	if got := sanitizePathSegment("con"); got != "_con" {
		t.Fatalf("unexpected reserved basename handling: %q", got)
	}
	if got := sanitizePathSegment(""); got != "feishu-doc" {
		t.Fatalf("unexpected fallback: %q", got)
	}
}

func TestCreateOutputLayout(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	layout, err := createOutputLayout(baseDir, "示例文档（演示版） - 飞书云文档", false)
	if err != nil {
		t.Fatalf("createOutputLayout failed: %v", err)
	}
	defer layout.cleanup()

	if filepath.Base(layout.rootDir) != "示例文档（演示版）" && filepath.Base(layout.rootDir) != "示例文档(演示版)" {
		t.Fatalf("unexpected root dir basename: %q", filepath.Base(layout.rootDir))
	}
	if layout.reportPath != "" {
		t.Fatalf("report path should be empty when debug is disabled")
	}
	if _, err := os.Stat(layout.workingRoot); err != nil {
		t.Fatalf("working root missing: %v", err)
	}
	if err := layout.cleanup(); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(layout.workingRoot); !os.IsNotExist(err) {
		t.Fatalf("expected working root to be removed, got %v", err)
	}
}

func TestBuildSheetFragment(t *testing.T) {
	t.Parallel()

	ctx := &buildContext{
		blocksByID: map[string]StructuredBlockRecord{
			"25": {
				ID:   "25",
				Type: "sheet",
				Sheet: &StructuredSheetData{
					RowCount: 3,
					ColCount: 2,
					Cells: []StructuredSheetCell{
						{Row: 0, Col: 0, Text: "更新日志", Style: StructuredSheetCellStyle{Font: "bold 14pt", HAlign: 1, BackColor: "#f6f8fc"}},
						{Row: 1, Col: 0, Text: "日期", Style: StructuredSheetCellStyle{Font: "bold 14pt"}},
						{Row: 1, Col: 1, Text: "版本号", Style: StructuredSheetCellStyle{Font: "bold 14pt", BackColor: "#e1eaff"}},
						{Row: 2, Col: 1, Text: "One Pro"},
					},
					Spans: []StructuredSheetSpan{
						{Row: 0, Col: 0, RowCount: 1, ColCount: 2},
						{Row: 1, Col: 0, RowCount: 2, ColCount: 1},
					},
				},
			},
		},
	}

	got, err := buildSheetFragment(ctx, "25")
	if err != nil {
		t.Fatalf("buildSheetFragment failed: %v", err)
	}

	checks := []string{
		`<table class="restored-sheet-table">`,
		`colspan="2"`,
		`rowspan="2"`,
		`更新日志`,
		`One Pro`,
		`background:#e1eaff`,
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("expected fragment to contain %q, got %s", check, got)
		}
	}
}

func TestBuildCodeFragment(t *testing.T) {
	t.Parallel()

	html := "<div class=\"block docx-code-block\" data-block-type=\"code\" data-block-id=\"9\"><div class=\"code-block-header\"><div class=\"code-block-caption\"><div class=\"ace-line\"><span>Code block</span></div></div><div class=\"code-block-header-toolbar\"><button class=\"code-block-header-btn\"><span>TypeScript</span></button></div></div><div class=\"code-block-content\"><div class=\"zone-container code-block-zone-container\"><div class=\"ace-line\"><div class=\"code-line-wrapper\"><span class=\"code-hljs-keyword\">const</span><span> value = </span><span class=\"code-hljs-number\">1</span><span>;</span></div></div><div class=\"ace-line\"><div class=\"code-line-wrapper\"><span>console.log(value)</span></div></div></div></div></div>"
	got, err := buildCodeFragment(html, "9")
	if err != nil {
		t.Fatalf("buildCodeFragment failed: %v", err)
	}

	checks := []string{
		`restored-code-block`,
		`restored-code-header`,
		`restored-code-caption`,
		`restored-code-copy`,
		`data-copy-code`,
		`TypeScript`,
		`language-typescript`,
		`token-keyword`,
		`token-number`,
		`const`,
		`value = `,
		`1`,
		`console.log(value)`,
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("expected code fragment to contain %q, got %s", check, got)
		}
	}
}

func TestBuildHeadingFragment(t *testing.T) {
	t.Parallel()

	ctx := &buildContext{}
	html := "<div class=\"block docx-heading2-block\" data-block-type=\"heading2\" data-block-id=\"28\"><div class=\"heading-block\"><div class=\"heading heading-h2\"><div class=\"heading-order\">1.1</div><div class=\"heading-content\"><div class=\"ace-line\"><span>安装和激活</span></div></div></div></div></div>"
	got, err := buildHeadingFragment(ctx, html, "28", 3)
	if err != nil {
		t.Fatalf("buildHeadingFragment failed: %v", err)
	}

	checks := []string{
		`<h3 class="restored-heading restored-heading-level-3">`,
		`restored-heading-order`,
		`1.1`,
		`安装和激活`,
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("expected heading fragment to contain %q, got %s", check, got)
		}
	}
	if strings.Contains(got, `<span class="restored-heading-text"><div>`) {
		t.Fatalf("expected heading fragment to render inline heading content, got %s", got)
	}
}

func TestBuildHeadingFragmentRendersChildren(t *testing.T) {
	t.Parallel()

	ctx := &buildContext{}
	html := "<div class=\"block docx-heading4-block\" data-block-type=\"heading4\" data-block-id=\"97\"><div class=\"heading-block\"><div class=\"heading heading-h4\"><div class=\"heading-order\">2.</div><div class=\"heading-content\"><div class=\"ace-line\"><span>对扩展进行设置</span></div></div></div><div class=\"heading-children\"><div class=\"render-unit-wrapper\"><div class=\"block docx-text-block\" data-block-type=\"text\" data-block-id=\"249\"><div class=\"text-block\"><div class=\"zone-container text-editor\"><div class=\"ace-line\"><span>展开查看详细步骤</span></div></div></div></div><div class=\"block docx-heading5-block\" data-block-type=\"heading5\" data-block-id=\"250\"><div class=\"heading-block\"><div class=\"heading heading-h5\"><div class=\"heading-content\"><div class=\"ace-line\"><span>A. 如果你使用Chrome：</span></div></div></div></div></div></div></div></div></div>"
	got, err := buildHeadingFragment(ctx, html, "97", 5)
	if err != nil {
		t.Fatalf("buildHeadingFragment failed: %v", err)
	}

	for _, check := range []string{
		`restored-heading-level-5`,
		`restored-heading-children`,
		`展开查看详细步骤`,
		`A. 如果你使用Chrome：`,
	} {
		if !strings.Contains(got, check) {
			t.Fatalf("expected nested heading fragment to contain %q, got %s", check, got)
		}
	}
}

func TestBuildQuoteContainerFragment(t *testing.T) {
	t.Parallel()

	ctx := &buildContext{}
	html := `<div class="block docx-quote_container-block" data-block-type="quote_container" data-block-id="231"><div class="quote-container-block"><div class="quote-container-block-children"><div class="render-unit-wrapper quote-container-render-unit"><div class="block docx-text-block" data-block-type="text" data-block-id="372"><div class="text-block"><div class="zone-container text-editor"><div class="ace-line"><span>这是一段引用说明。</span></div></div></div></div></div></div></div></div>`
	got, err := buildQuoteContainerFragment(ctx, html, "231")
	if err != nil {
		t.Fatalf("buildQuoteContainerFragment failed: %v", err)
	}

	for _, check := range []string{
		`<blockquote class="block restored-quote-block"`,
		`data-block-id="231"`,
		`restored-quote-body`,
		`这是一段引用说明。`,
	} {
		if !strings.Contains(got, check) {
			t.Fatalf("expected quote fragment to contain %q, got %s", check, got)
		}
	}
}

func TestBuildFileFragment(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	renderedDir := filepath.Join(tempDir, "rendered")
	if err := os.MkdirAll(renderedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	pngData, err := decodeBase64Fixture("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4////fwAJ+wP9KobjigAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatalf("decodeBase64Fixture failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(renderedDir, "42.png"), pngData, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	ctx := &buildContext{
		renderedImagesDir: renderedDir,
		inlineCache:       map[string]string{},
	}
	html := `<div class="block docx-file-block" data-block-type="file" data-block-id="42"><div class="file-container" style="width: 812px;"></div><video src="https://example.com/video.mp4"></video><div>00:00</div><div>/</div><div>09:49</div><div>1080p</div><div>1x</div><div>Video 2025-10-29 20.46.24.mov</div><div>09:49</div></div>`
	got, err := buildFileFragment(ctx, html, "42")
	if err != nil {
		t.Fatalf("buildFileFragment failed: %v", err)
	}

	for _, check := range []string{
		`restored-file-block`,
		`restored-file-video-player`,
		`poster="data:image/png;base64,`,
		`Video 2025-10-29 20.46.24.mov`,
		`09:49`,
		`打开源文件`,
		`--file-max-width: 812px;`,
	} {
		if !strings.Contains(got, check) {
			t.Fatalf("expected file fragment to contain %q, got %s", check, got)
		}
	}
}

func TestBuildTextFragment(t *testing.T) {
	t.Parallel()

	ctx := &buildContext{}
	html := `<div class="block docx-text-block" data-block-type="text" data-block-id="8"><div class="text-block-wrapper"><div class="text-block"><div class="zone-container text-editor hide-placeholder non-empty" style="text-align: center;"><div class="ace-line"><span>一段正文</span></div></div></div></div></div>`
	got, err := buildTextFragment(ctx, html, "8")
	if err != nil {
		t.Fatalf("buildTextFragment failed: %v", err)
	}

	checks := []string{
		`<div class="block restored-text-block" data-block-id="8" data-block-type="text">`,
		`<p class="restored-text" style="text-align:center">`,
		`一段正文`,
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("expected text fragment to contain %q, got %s", check, got)
		}
	}
	if strings.Contains(got, `<p class="restored-text" style="text-align:center"><div>`) {
		t.Fatalf("expected text fragment to render inline paragraph content, got %s", got)
	}
}

func TestRenderPageTemplateIncludesImageLightbox(t *testing.T) {
	t.Parallel()

	got := renderPageTemplate("示例标题", `<figure class="restored-image"><img src="https://example.com/a.png" alt="示例图片" /></figure>`)

	for _, check := range []string{
		`id="image-lightbox"`,
		`class="image-lightbox-image"`,
		`document.addEventListener("click"`,
		`const openLightbox = (img) =>`,
		`data-copy-code`,
		`navigator.clipboard`,
		`已复制`,
		`.doc-page img { cursor: zoom-in; }`,
	} {
		if !strings.Contains(got, check) {
			t.Fatalf("expected renderPageTemplate to contain %q, got %s", check, got)
		}
	}
}

func TestLocalizeReplicaVideosDownloadsLocalFiles(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("fake-video"))
	}))
	defer server.Close()

	baseDir := t.TempDir()
	layout, err := createOutputLayout(baseDir, "示例文档", false)
	if err != nil {
		t.Fatalf("createOutputLayout failed: %v", err)
	}
	defer layout.cleanup()

	html := `<!doctype html><html><body><article class="doc-page"><div class="block restored-file-block" data-block-id="42" data-block-type="view"><figure class="restored-file restored-file-video"><div class="restored-file-preview"><video class="restored-file-video-player" controls playsinline preload="metadata"><source src="` + server.URL + `/video.mp4" /></video></div><figcaption class="restored-file-caption"><div class="restored-file-name">Sample Demo.mov</div><div class="restored-file-meta"><a class="restored-file-link" href="` + server.URL + `/video.mp4">打开源文件</a></div></figcaption></figure></div></article></body></html>`
	if err := os.WriteFile(layout.workingReplicaHtmlPath, []byte(html), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if err := localizeReplicaVideos(layout, nil); err != nil {
		t.Fatalf("localizeReplicaVideos failed: %v", err)
	}
	if err := syncReplicaAssets(layout); err != nil {
		t.Fatalf("syncReplicaAssets failed: %v", err)
	}

	got, err := os.ReadFile(layout.workingReplicaHtmlPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	for _, check := range []string{
		`示例文档-assets/Sample-Demo.mp4`,
		`打开本地视频`,
		`download=""`,
	} {
		if !strings.Contains(string(got), check) {
			t.Fatalf("expected localized html to contain %q, got %s", check, string(got))
		}
	}

	for _, path := range []string{
		filepath.Join(layout.workingReplicaAssetsDir, "Sample-Demo.mp4"),
		filepath.Join(layout.replicaAssetsDir, "Sample-Demo.mp4"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected localized asset at %s: %v", path, err)
		}
	}
}

func TestWriteManifest(t *testing.T) {
	t.Parallel()

	layout, err := createOutputLayout(t.TempDir(), "示例文档", false)
	if err != nil {
		t.Fatalf("createOutputLayout failed: %v", err)
	}
	defer layout.cleanup()

	result := contracts.Result{
		Ok:              true,
		Title:           "示例文档",
		TitleRaw:        "示例文档 - Feishu Docs",
		OutputDir:       layout.rootDir,
		SelectedOutputs: []string{"html", "md"},
		Files: []contracts.FileArtifact{
			{Kind: "html", Path: filepath.Join(layout.rootDir, "示例文档.html")},
			{Kind: "assets", Path: filepath.Join(layout.rootDir, "示例文档-assets")},
		},
		Stats: map[string]int{"blocks": 12, "embeddedImages": 3},
	}
	metadata := CaptureMetadata{
		URL:           "https://example.com/doc",
		FinalURL:      "https://example.com/doc?token=1",
		CapturedAt:    "2026-04-15T12:00:00Z",
		ReadySelector: ".page-main-item.editor",
		UsedPassword:  true,
	}

	if err := os.MkdirAll(layout.replicaAssetsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.replicaAssetsDir, "demo.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if err := writeManifest(layout, result, metadata); err != nil {
		t.Fatalf("writeManifest failed: %v", err)
	}

	got, err := os.ReadFile(layout.manifestPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	for _, check := range []string{
		`"title": "示例文档"`,
		`"kind": "assets"`,
		`"readySelector": ".page-main-item.editor"`,
		`"usedPassword": true`,
		`"relativePath": "示例文档-assets/demo.mp4"`,
		`"kind": "video"`,
	} {
		if !strings.Contains(string(got), check) {
			t.Fatalf("expected manifest to contain %q, got %s", check, string(got))
		}
	}
}

func TestBuildDividerFragment(t *testing.T) {
	t.Parallel()

	got := buildDividerFragment("26")
	checks := []string{
		`<hr class="block restored-divider"`,
		`data-block-id="26"`,
		`data-block-type="divider"`,
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("expected divider fragment to contain %q, got %s", check, got)
		}
	}
}

func TestBuildOrderedListFragment(t *testing.T) {
	t.Parallel()

	ctx := &buildContext{}
	tasks := []renderTask{
		{
			BlockID:   "31",
			BlockType: "ordered",
			HTML:      "<div class=\"block docx-ordered-block\" data-block-type=\"ordered\" data-block-id=\"31\"><div class=\"list-wrapper ordered-list\"><div class=\"list\"><div class=\"order\">1.</div><div class=\"list-content\"><div class=\"ace-line\"><span>第一项</span></div></div></div></div></div>",
		},
		{
			BlockID:   "32",
			BlockType: "ordered",
			HTML:      "<div class=\"block docx-ordered-block\" data-block-type=\"ordered\" data-block-id=\"32\"><div class=\"list-wrapper ordered-list\"><div class=\"list\"><div class=\"order\">2.</div><div class=\"list-content\"><div class=\"ace-line\"><span>第二项</span></div></div></div></div></div>",
		},
	}

	got, err := buildOrderedListFragment(ctx, tasks)
	if err != nil {
		t.Fatalf("buildOrderedListFragment failed: %v", err)
	}

	checks := []string{
		`<ol class="block restored-ordered-list">`,
		`<li class="restored-list-item" data-block-id="31" value="1">`,
		`第一项`,
		`第二项`,
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("expected ordered list fragment to contain %q, got %s", check, got)
		}
	}
	if strings.Contains(got, `<li class="restored-list-item" data-block-id="31" value="1"><div>`) {
		t.Fatalf("expected ordered list fragment to render inline list content, got %s", got)
	}
}

func TestPrepareGenericSelectionNormalizesInlineSemantics(t *testing.T) {
	t.Parallel()

	docHTML := `<div class="list-content"><div class="ace-line"><span class="author-123">打开</span><span class="inline-code inline-code_start inline-code_end"><span>工具-&gt;插件</span></span><span>，下载</span><span class="mention-doc-embed-container"><a class="mention-doc" href="https://example.com/file" target="_self"><span class="embed-inline-link"><span><span class="text embed-text-container">demo.xpi</span></span></span></a></span><span class="outer-u-container docx-outer-link-container"><span class="link-wrapper"><a class="link" href="https://example.com/docs" target="_self"><span>文档链接</span></a></span></span><span class="text-highlight-background-yellow-light-bg">高亮</span><span class="textHighlight textHighlight-pink-text textHighlight-ccmtoken-doc-textcolor-red">红字</span><span style="margin: 0px 0.1px;"><span>纯文本</span></span><div class="block docx-text-block isEmpty"><div class="ace-line">​</div></div></div></div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(docHTML))
	if err != nil {
		t.Fatalf("NewDocumentFromReader failed: %v", err)
	}
	root := doc.Find(".list-content").First()
	ctx := &buildContext{}

	if err := prepareGenericSelection(ctx, root); err != nil {
		t.Fatalf("prepareGenericSelection failed: %v", err)
	}
	got, err := root.Html()
	if err != nil {
		t.Fatalf("root.Html failed: %v", err)
	}

	checks := []string{
		`<code>工具-&gt;插件</code>`,
		`<a class="mention-doc" href="https://example.com/file" target="_blank" rel="noopener noreferrer">demo.xpi</a>`,
		`<a class="link" href="https://example.com/docs" target="_blank" rel="noopener noreferrer">文档链接</a>`,
		`<mark>高亮</mark>`,
		`<span style="color:#ef574d">红字</span>`,
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("expected normalized html to contain %q, got %s", check, got)
		}
	}
	for _, forbidden := range []string{"author-123", "margin: 0px 0.1px", `<span>纯文本</span>`, "textHighlight-pink-text"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("expected normalized html to drop %q, got %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "纯文本") {
		t.Fatalf("expected normalized html to keep text content, got %s", got)
	}
	if strings.Contains(got, "isEmpty") {
		t.Fatalf("expected empty blocks to be removed, got %s", got)
	}
}
