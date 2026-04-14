package capture

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	xhtml "golang.org/x/net/html"
)

var removeSelectors = []string{
	".layer-popup",
	".bear-virtual-renderUnit-placeholder",
	".bear-virtual-pre-renderer",
	".docx-block-zero-space",
	".gpf-biz-action-manager-forbidden-placeholder",
	".block-area-comment-container",
	".grid-column-percent",
	".fold-wrapper",
	".doc-info-swipe-container",
	".doc-ai-summary-mount-point",
	".doc-meta-entry-container",
	".page-block-header-top",
	"[data-zero-space='true']",
	"[data-enter='true']",
}

var stripAttrs = map[string]struct{}{
	"contenteditable":          {},
	"crossorigin":              {},
	"data-need-render-loading": {},
	"data-node":                {},
	"data-slate-editor":        {},
	"dir":                      {},
	"draggable":                {},
	"selectionapi":             {},
	"spellcheck":               {},
	"tabindex":                 {},
}

var keepDataAttrs = map[string]struct{}{
	"data-block-id":   {},
	"data-block-type": {},
}

var nonHTMLBlockTypes = map[string]struct{}{"sheet": {}}
var widthPxPattern = regexp.MustCompile(`width:\s*([0-9.]+)px`)
var gridPercentPattern = regexp.MustCompile(`width:\s*calc\(([0-9.]+)%`)
var singleBlockWrapperPattern = regexp.MustCompile(`(?is)^<\s*(div|p)(?:\s+[^>]*)?>(.*)</\s*([a-z0-9]+)\s*>$`)

type buildContext struct {
	rootDir           string
	reportPath        string
	blocksDir         string
	renderedImagesDir string
	metadataPath      string
	blocks            []StructuredBlockRecord
	blocksByID        map[string]StructuredBlockRecord
	inlineCache       map[string]string
	assetMap          map[string]string
}

type renderTask struct {
	BlockID   string
	BlockType string
	HTML      string
}

func buildReplicaHTML(layout *outputLayout) error {
	ctx, err := createBuildContext(layout)
	if err != nil {
		return err
	}
	topLevelIDs, err := computeTopLevelBlockIDs(ctx)
	if err != nil {
		return err
	}

	tasks := make([]renderTask, 0, len(ctx.blocks))
	for _, block := range ctx.blocks {
		if !topLevelIDs[block.ID] {
			continue
		}
		htmlPath := filepath.Join(ctx.blocksDir, block.ID+".html")
		html, err := os.ReadFile(htmlPath)
		if err != nil {
			continue
		}
		tasks = append(tasks, renderTask{
			BlockID:   block.ID,
			BlockType: block.Type,
			HTML:      string(html),
		})
	}

	rendered, err := renderBlockSequence(ctx, tasks)
	if err != nil {
		return err
	}

	title, err := loadTitle(ctx)
	if err != nil {
		return err
	}
	pageHTML := renderPageTemplate(title, strings.Join(rendered, "\n"))
	return os.WriteFile(layout.workingReplicaHtmlPath, []byte(pageHTML), 0o644)
}

func createBuildContext(layout *outputLayout) (*buildContext, error) {
	blocks, err := loadStructuredBlocks(layout.blocksJsonPath)
	if err != nil {
		return nil, err
	}
	entries, _ := os.ReadDir(layout.assetsDir)
	assetMap := map[string]string{}
	blocksByID := map[string]StructuredBlockRecord{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		blockID := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if _, ok := assetMap[blockID]; !ok {
			assetMap[blockID] = filepath.Join(layout.assetsDir, entry.Name())
		}
	}
	for _, block := range blocks {
		blocksByID[block.ID] = block
	}
	rootDir, _ := filepath.Abs(layout.rootDir)
	return &buildContext{
		rootDir:           rootDir,
		reportPath:        layout.reportPath,
		blocksDir:         layout.blocksDir,
		renderedImagesDir: layout.renderedImagesDir,
		metadataPath:      layout.metadataPath,
		blocks:            blocks,
		blocksByID:        blocksByID,
		inlineCache:       map[string]string{},
		assetMap:          assetMap,
	}, nil
}

func computeTopLevelBlockIDs(ctx *buildContext) (map[string]bool, error) {
	descendantsByID := map[string]map[string]bool{}
	re := regexp.MustCompile(`data-block-id="([^"]+)"`)
	for _, block := range ctx.blocks {
		htmlPath := filepath.Join(ctx.blocksDir, block.ID+".html")
		html, err := os.ReadFile(htmlPath)
		if err != nil {
			descendantsByID[block.ID] = map[string]bool{}
			continue
		}
		matches := re.FindAllStringSubmatch(string(html), -1)
		set := map[string]bool{}
		for _, match := range matches {
			if len(match) > 1 {
				set[match[1]] = true
			}
		}
		descendantsByID[block.ID] = set
	}

	parents := map[string]string{}
	for _, block := range ctx.blocks {
		if block.ID == "1" {
			continue
		}
		candidates := []string{}
		for candidateID, descendants := range descendantsByID {
			if candidateID != block.ID && descendants[block.ID] {
				candidates = append(candidates, candidateID)
			}
		}
		if len(candidates) == 0 {
			parents[block.ID] = ""
			continue
		}
		best := candidates[0]
		bestSize := len(descendantsByID[best])
		for _, candidate := range candidates[1:] {
			size := len(descendantsByID[candidate])
			if size < bestSize || (size == bestSize && compareBlockIDs(candidate, best) < 0) {
				best = candidate
				bestSize = size
			}
		}
		parents[block.ID] = best
	}

	topLevel := map[string]bool{}
	for _, block := range ctx.blocks {
		if block.ID == "1" {
			continue
		}
		parentID := parents[block.ID]
		if parentID == "1" || parentID == "" {
			topLevel[block.ID] = true
		}
	}
	return topLevel, nil
}

func renderBlockFragment(ctx *buildContext, html, expectedID string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		root = doc.Selection.Children().First()
	}
	if root.Length() == 0 {
		return html, nil
	}

	blockID, _ := root.Attr("data-block-id")
	if blockID == "" {
		blockID = expectedID
	}
	blockType, _ := root.Attr("data-block-type")

	switch blockType {
	case "grid":
		return buildGridFragment(ctx, selectionOuterHTML(root), blockID)
	case "image":
		return buildImageFragment(ctx, selectionOuterHTML(root), blockID)
	case "view":
		return buildViewFragment(ctx, selectionOuterHTML(root), blockID)
	case "file":
		return buildFileFragment(ctx, selectionOuterHTML(root), blockID)
	case "text":
		return buildTextFragment(ctx, selectionOuterHTML(root), blockID)
	case "callout":
		return buildCalloutFragment(ctx, selectionOuterHTML(root), blockID)
	case "quote_container":
		return buildQuoteContainerFragment(ctx, selectionOuterHTML(root), blockID)
	case "divider":
		return buildDividerFragment(blockID), nil
	case "code":
		if fragment, err := buildCodeFragment(selectionOuterHTML(root), blockID); err == nil && fragment != "" {
			return fragment, nil
		}
	case "heading1":
		return buildHeadingFragment(ctx, selectionOuterHTML(root), blockID, 2)
	case "heading2":
		return buildHeadingFragment(ctx, selectionOuterHTML(root), blockID, 3)
	case "heading3":
		return buildHeadingFragment(ctx, selectionOuterHTML(root), blockID, 4)
	case "heading4":
		return buildHeadingFragment(ctx, selectionOuterHTML(root), blockID, 5)
	case "heading5":
		return buildHeadingFragment(ctx, selectionOuterHTML(root), blockID, 6)
	case "sheet":
		if fragment, err := buildSheetFragment(ctx, blockID); err == nil && fragment != "" {
			return fragment, nil
		}
	}
	if _, ok := screenshotFirstBlockTypes[blockType]; ok {
		if visual, err := buildVisualFragment(ctx, blockID, blockType); err == nil && visual != "" {
			return visual, nil
		}
	}
	if _, ok := nonHTMLBlockTypes[blockType]; ok {
		if visual, err := buildVisualFragment(ctx, blockID, blockType); err == nil && visual != "" {
			return visual, nil
		}
	}
	return cleanupGenericFragment(ctx, root)
}

func renderBlockSequence(ctx *buildContext, tasks []renderTask) ([]string, error) {
	rendered := make([]string, 0, len(tasks))
	for index := 0; index < len(tasks); index++ {
		task := tasks[index]
		if task.BlockType == "ordered" {
			start := index
			for index+1 < len(tasks) && tasks[index+1].BlockType == "ordered" {
				index++
			}
			listHTML, err := buildOrderedListFragment(ctx, tasks[start:index+1])
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(listHTML) != "" {
				rendered = append(rendered, listHTML)
			}
			continue
		}

		fragment, err := renderBlockFragment(ctx, task.HTML, task.BlockID)
		if err != nil {
			return nil, err
		}
		rendered = append(rendered, fragment)
	}
	return rendered, nil
}

func buildTextFragment(ctx *buildContext, html, expectedID string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		return html, nil
	}
	if root.HasClass("isEmpty") || strings.TrimSpace(strings.ReplaceAll(root.Text(), "\u200b", "")) == "" {
		return "", nil
	}

	content := root.Find(".zone-container, .caption-editor").First()
	if content.Length() == 0 {
		content = root
	}
	if err := prepareGenericSelection(ctx, content); err != nil {
		return "", err
	}
	contentHTML, err := extractLineHTML(content)
	if err != nil {
		return "", err
	}
	contentHTML, err = normalizeInlineBlockHTML(contentHTML)
	if err != nil {
		return "", err
	}
	contentHTML = strings.TrimSpace(contentHTML)
	if contentHTML == "" {
		return "", nil
	}

	styleAttr := ""
	if style, ok := content.Attr("style"); ok {
		if align := extractTextAlign(style); align != "" {
			styleAttr = fmt.Sprintf(` style="text-align:%s"`, align)
		}
	}

	return fmt.Sprintf(`<div class="block restored-text-block" data-block-id="%s" data-block-type="text"><p class="restored-text"%s>%s</p></div>`,
		escapeHTML(expectedID), styleAttr, contentHTML), nil
}

func buildDividerFragment(expectedID string) string {
	return fmt.Sprintf(`<hr class="block restored-divider" data-block-id="%s" data-block-type="divider" />`,
		escapeHTML(expectedID))
}

func buildGridFragment(ctx *buildContext, html, expectedID string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		return html, nil
	}
	renderUnit := root.Find(".grid-render-unit").First()
	columns := renderUnit.ChildrenFiltered(`[data-block-type='grid_column']`)
	if columns.Length() == 0 {
		return cleanupGenericFragment(ctx, root)
	}

	columnHTML := []string{}
	for _, node := range columns.Nodes {
		column := goquery.NewDocumentFromNode(node).Selection
		columnID, _ := column.Attr("data-block-id")
		styleAttr, _ := column.Attr("style")
		basis := parseGridPercent(styleAttr)
		style := ""
		if basis > 0 {
			style = fmt.Sprintf(` style="--column-grow: %.4g;"`, basis)
		}
		sourceRoot := column
		gridColumn := column.ChildrenFiltered(".grid-column-block").First()
		if gridColumn.Length() > 0 {
			contentRoot := gridColumn.ChildrenFiltered(".render-unit-wrapper").First()
			if contentRoot.Length() > 0 {
				sourceRoot = contentRoot
			} else {
				sourceRoot = gridColumn
			}
		}
		childTasks := []renderTask{}
		sourceRoot.Children().Each(func(_ int, child *goquery.Selection) {
			childID, _ := child.Attr("data-block-id")
			if childID == "" {
				return
			}
			childType, _ := child.Attr("data-block-type")
			childTasks = append(childTasks, renderTask{
				BlockID:   childID,
				BlockType: childType,
				HTML:      selectionOuterHTML(child),
			})
		})
		childFragments, err := renderBlockSequence(ctx, childTasks)
		if err != nil {
			return "", err
		}
		columnHTML = append(columnHTML, fmt.Sprintf(
			`<div class="restored-grid-column" data-block-id="%s" data-block-type="grid_column"%s>%s</div>`,
			escapeHTML(columnID), style, strings.Join(childFragments, "\n"),
		))
	}

	return fmt.Sprintf(`<div class="block restored-grid-block" data-block-id="%s" data-block-type="grid"><div class="restored-grid-track">%s</div></div>`,
		escapeHTML(expectedID), strings.Join(columnHTML, "\n")), nil
}

func buildCalloutFragment(ctx *buildContext, html, expectedID string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		return html, nil
	}

	icon := strings.TrimSpace(strings.ReplaceAll(root.Find(".callout-block-emoji").First().Text(), "\u200b", ""))
	bodyRoot := root.Find(".callout-block-children .render-unit-wrapper").First()
	if bodyRoot.Length() == 0 {
		bodyRoot = root.Find(".callout-block-children").First()
	}
	if bodyRoot.Length() == 0 {
		return cleanupGenericFragment(ctx, root)
	}

	childTasks := []renderTask{}
	bodyRoot.ChildrenFiltered(`[data-block-id]`).Each(func(_ int, child *goquery.Selection) {
		childID, _ := child.Attr("data-block-id")
		childType, _ := child.Attr("data-block-type")
		if childID == "" {
			return
		}
		childTasks = append(childTasks, renderTask{
			BlockID:   childID,
			BlockType: childType,
			HTML:      selectionOuterHTML(child),
		})
	})
	if len(childTasks) == 0 {
		return cleanupGenericFragment(ctx, root)
	}

	bodyFragments, err := renderBlockSequence(ctx, childTasks)
	if err != nil {
		return "", err
	}

	iconHTML := ""
	if icon != "" {
		iconHTML = fmt.Sprintf(`<div class="restored-callout-emoji">%s</div>`, escapeHTML(icon))
	}

	return strings.TrimSpace(fmt.Sprintf(`
<div class="block restored-callout-block" data-block-id="%s" data-block-type="callout">
  <div class="restored-callout-shell">
    %s
    <div class="restored-callout-body">%s</div>
  </div>
</div>`, escapeHTML(expectedID), iconHTML, strings.Join(bodyFragments, "\n"))), nil
}

func buildQuoteContainerFragment(ctx *buildContext, html, expectedID string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		return html, nil
	}

	bodyRoot := root.Find(".quote-container-block-children .render-unit-wrapper").First()
	if bodyRoot.Length() == 0 {
		bodyRoot = root.Find(".quote-container-block-children").First()
	}
	if bodyRoot.Length() == 0 {
		return cleanupGenericFragment(ctx, root)
	}

	childTasks := []renderTask{}
	bodyRoot.ChildrenFiltered(`[data-block-id]`).Each(func(_ int, child *goquery.Selection) {
		childID, _ := child.Attr("data-block-id")
		childType, _ := child.Attr("data-block-type")
		if childID == "" {
			return
		}
		childTasks = append(childTasks, renderTask{
			BlockID:   childID,
			BlockType: childType,
			HTML:      selectionOuterHTML(child),
		})
	})
	if len(childTasks) == 0 {
		return cleanupGenericFragment(ctx, root)
	}

	bodyFragments, err := renderBlockSequence(ctx, childTasks)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(fmt.Sprintf(`
<blockquote class="block restored-quote-block" data-block-id="%s" data-block-type="quote">
  <div class="restored-quote-body">%s</div>
</blockquote>`, escapeHTML(expectedID), strings.Join(bodyFragments, "\n"))), nil
}

func buildHeadingFragment(ctx *buildContext, html, expectedID string, level int) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		return html, nil
	}

	content := root.Find(".heading-content").First()
	if content.Length() == 0 {
		content = root
	}
	if err := prepareGenericSelection(ctx, content); err != nil {
		return "", err
	}
	contentHTML, err := extractLineHTML(content)
	if err != nil {
		return "", err
	}
	contentHTML, err = normalizeInlineBlockHTML(contentHTML)
	if err != nil {
		return "", err
	}
	if contentHTML == "" {
		contentHTML = escapeHTML(strings.TrimSpace(strings.ReplaceAll(root.Text(), "\u200b", "")))
	}

	orderText := strings.TrimSpace(strings.ReplaceAll(root.Find(".heading-order").First().Text(), "\u200b", ""))
	orderHTML := ""
	if orderText != "" {
		orderHTML = fmt.Sprintf(`<span class="restored-heading-order">%s</span> `, escapeHTML(orderText))
	}

	tagName := "h2"
	switch level {
	case 2:
		tagName = "h2"
	case 3:
		tagName = "h3"
	case 4:
		tagName = "h4"
	case 5:
		tagName = "h5"
	default:
		tagName = "h6"
	}

	childHTML := ""
	bodyRoot := root.Find(".heading-children > .render-unit-wrapper").First()
	if bodyRoot.Length() == 0 {
		bodyRoot = root.Find(".heading-children").First()
	}
	if bodyRoot.Length() > 0 {
		childTasks := []renderTask{}
		bodyRoot.ChildrenFiltered(`[data-block-id][data-block-type]`).Each(func(_ int, child *goquery.Selection) {
			childID, _ := child.Attr("data-block-id")
			childType, _ := child.Attr("data-block-type")
			if childID == "" {
				return
			}
			childTasks = append(childTasks, renderTask{
				BlockID:   childID,
				BlockType: childType,
				HTML:      selectionOuterHTML(child),
			})
		})
		if len(childTasks) > 0 {
			childFragments, err := renderBlockSequence(ctx, childTasks)
			if err != nil {
				return "", err
			}
			if len(childFragments) > 0 {
				childHTML = fmt.Sprintf(`<div class="restored-heading-children">%s</div>`, strings.Join(childFragments, "\n"))
			}
		}
	}

	return strings.TrimSpace(fmt.Sprintf(`
<section class="block restored-heading-block" data-block-id="%s" data-block-type="heading">
  <%s class="restored-heading restored-heading-level-%d">%s<span class="restored-heading-text">%s</span></%s>
  %s
</section>`, escapeHTML(expectedID), tagName, level, orderHTML, contentHTML, tagName, childHTML)), nil
}

func buildOrderedListFragment(ctx *buildContext, tasks []renderTask) (string, error) {
	if len(tasks) == 0 {
		return "", nil
	}

	items := make([]string, 0, len(tasks))
	start := 0
	for index, task := range tasks {
		itemHTML, order, err := buildOrderedItemFragment(ctx, task.HTML, task.BlockID)
		if err != nil {
			return "", err
		}
		if index == 0 {
			start = order
		}
		if strings.TrimSpace(itemHTML) != "" {
			items = append(items, itemHTML)
		}
	}
	if len(items) == 0 {
		return "", nil
	}

	startAttr := ""
	if start > 1 {
		startAttr = fmt.Sprintf(` start="%d"`, start)
	}
	return fmt.Sprintf(`<ol class="block restored-ordered-list"%s>%s</ol>`, startAttr, strings.Join(items, "")), nil
}

func buildOrderedItemFragment(ctx *buildContext, html, expectedID string) (string, int, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", 0, err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		return "", 0, nil
	}

	orderText := strings.TrimSpace(strings.ReplaceAll(root.Find(".order").First().Text(), "\u200b", ""))
	orderValue := parseOrderedMarker(orderText)

	content := root.Find(".list-content").First()
	if content.Length() == 0 {
		content = root
	}
	if err := prepareGenericSelection(ctx, content); err != nil {
		return "", 0, err
	}
	contentHTML, err := extractLineHTML(content)
	if err != nil {
		return "", 0, err
	}
	contentHTML, err = normalizeInlineBlockHTML(contentHTML)
	if err != nil {
		return "", 0, err
	}
	if contentHTML == "" {
		contentHTML = escapeHTML(strings.TrimSpace(strings.ReplaceAll(root.Text(), "\u200b", "")))
	}

	valueAttr := ""
	if orderValue > 0 {
		valueAttr = fmt.Sprintf(` value="%d"`, orderValue)
	}
	return fmt.Sprintf(`<li class="restored-list-item" data-block-id="%s"%s>%s</li>`, escapeHTML(expectedID), valueAttr, contentHTML), orderValue, nil
}

func parseOrderedMarker(value string) int {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, ".、)")
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func extractLineHTML(root *goquery.Selection) (string, error) {
	lines := []string{}
	root.Find(".ace-line").Each(func(_ int, line *goquery.Selection) {
		html, err := line.Html()
		if err == nil {
			trimmed := strings.TrimSpace(strings.ReplaceAll(html, "\u200b", ""))
			if trimmed != "" {
				lines = append(lines, trimmed)
			}
		}
	})
	if len(lines) > 0 {
		return strings.Join(lines, "<br />"), nil
	}

	html, err := root.Html()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.ReplaceAll(html, "\u200b", "")), nil
}

func normalizeInlineBlockHTML(html string) (string, error) {
	trimmed := strings.TrimSpace(html)
	if trimmed == "" {
		return "", nil
	}
	trimmed = collapseSingleBlockWrappers(trimmed)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<div id=\"inline-root\">" + trimmed + "</div>"))
	if err != nil {
		return trimmed, nil
	}
	root := doc.Find("#inline-root").First()
	if root.Length() == 0 {
		return trimmed, nil
	}

	children := root.Contents()
	if children.Length() == 0 {
		return trimmed, nil
	}

	lines := make([]string, 0, children.Length())
	blockLikeOnly := true
	children.Each(func(_ int, child *goquery.Selection) {
		if !blockLikeOnly {
			return
		}
		node := child.Get(0)
		if node == nil {
			return
		}
		if node.Type != xhtml.ElementNode {
			if strings.TrimSpace(strings.ReplaceAll(child.Text(), "\u200b", "")) == "" {
				return
			}
			blockLikeOnly = false
			return
		}
		switch strings.ToLower(goquery.NodeName(child)) {
		case "div", "p":
			inner, innerErr := child.Html()
			if innerErr != nil {
				blockLikeOnly = false
				return
			}
			inner = strings.TrimSpace(strings.ReplaceAll(inner, "\u200b", ""))
			if inner != "" {
				lines = append(lines, inner)
			}
		default:
			blockLikeOnly = false
		}
	})

	if blockLikeOnly && len(lines) > 0 {
		return collapseSingleBlockWrappers(strings.Join(lines, "<br />")), nil
	}
	return collapseSingleBlockWrappers(trimmed), nil
}

func collapseSingleBlockWrappers(value string) string {
	trimmed := strings.TrimSpace(value)
	for {
		match := singleBlockWrapperPattern.FindStringSubmatch(trimmed)
		if len(match) < 4 {
			return trimmed
		}
		if !strings.EqualFold(match[1], match[3]) {
			return trimmed
		}
		next := strings.TrimSpace(match[2])
		if next == "" || next == trimmed {
			return trimmed
		}
		trimmed = next
	}
}

func buildImageFragment(ctx *buildContext, html, expectedID string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	image := root.Find("img").First()
	if root.Length() == 0 || image.Length() == 0 {
		return cleanupGenericFragment(ctx, root)
	}

	widthWrapperStyle, _ := root.Find(".image-block-width-wrapper").First().Attr("style")
	width := parseWidthPX(widthWrapperStyle)
	if width == 0 {
		widthAttr, _ := image.Attr("width")
		if value, err := strconv.ParseFloat(widthAttr, 64); err == nil {
			width = value
		}
	}
	caption := strings.TrimSpace(strings.ReplaceAll(root.Find(".caption-editor-area").First().Text(), "\u200b", ""))
	alt, _ := image.Attr("alt")
	if strings.TrimSpace(alt) == "" {
		alt = "图片 " + expectedID
	}
	src, _ := image.Attr("src")
	bestSrc, err := bestImageSrcForBlock(ctx, expectedID, src)
	if err != nil {
		return "", err
	}
	figureStyle := ""
	if width > 0 {
		figureStyle = fmt.Sprintf(` style="--image-max-width: %.0fpx;"`, width)
	}
	captionHTML := ""
	if caption != "" {
		captionHTML = fmt.Sprintf(`<figcaption>%s</figcaption>`, escapeHTML(caption))
	}
	return strings.TrimSpace(fmt.Sprintf(`
<div class="block restored-image-block" data-block-id="%s" data-block-type="image">
  <figure class="restored-image"%s>
    <img src="%s" alt="%s" loading="lazy" />
    %s
  </figure>
</div>`, escapeHTML(expectedID), figureStyle, escapeHTML(bestSrc), escapeHTML(alt), captionHTML)), nil
}

func buildViewFragment(ctx *buildContext, html, expectedID string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		return html, nil
	}
	fileRoot := root.Find(`[data-block-type="file"][data-block-id]`).First()
	if fileRoot.Length() == 0 {
		return cleanupGenericFragment(ctx, root)
	}
	return buildFileFragmentFromSelection(ctx, fileRoot, expectedID, "view")
}

func buildFileFragment(ctx *buildContext, html, expectedID string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		return "", nil
	}
	return buildFileFragmentFromSelection(ctx, root, expectedID, "file")
}

func buildFileFragmentFromSelection(ctx *buildContext, root *goquery.Selection, expectedID string, blockType string) (string, error) {
	if root == nil || root.Length() == 0 {
		return "", nil
	}

	width := parseWidthPX(firstAttr(root, ".file-container", "style"))
	if width == 0 {
		width = parseWidthPX(firstAttr(root, ".preview-wrap", "style"))
	}

	videoSrc, _ := root.Find("video").First().Attr("src")
	previewSrc, err := bestFilePreviewSrc(ctx, root, expectedID)
	if err != nil {
		return "", err
	}
	title, duration := extractFileSummary(root)

	figureStyle := ""
	if width > 0 {
		figureStyle = fmt.Sprintf(` style="--file-max-width: %.0fpx;"`, width)
	}

	previewHTML := `<div class="restored-file-placeholder">文件预览</div>`
	switch {
	case strings.TrimSpace(videoSrc) != "":
		posterAttr := ""
		if previewSrc != "" {
			posterAttr = fmt.Sprintf(` poster="%s"`, escapeHTML(previewSrc))
		}
		previewHTML = fmt.Sprintf(`<video class="restored-file-video-player" controls playsinline preload="metadata"%s><source src="%s" />当前浏览器无法直接播放这个视频，请使用下方源文件链接打开。</video>`, posterAttr, escapeHTML(videoSrc))
	case previewSrc != "":
		alt := title
		if strings.TrimSpace(alt) == "" {
			alt = "文件预览 " + expectedID
		}
		previewHTML = fmt.Sprintf(`<img class="restored-file-poster" src="%s" alt="%s" loading="lazy" />`, escapeHTML(previewSrc), escapeHTML(alt))
	}

	titleHTML := ""
	if strings.TrimSpace(title) != "" {
		titleHTML = fmt.Sprintf(`<div class="restored-file-name">%s</div>`, escapeHTML(title))
	}

	metaParts := []string{}
	if duration != "" {
		metaParts = append(metaParts, fmt.Sprintf(`<span class="restored-file-duration">%s</span>`, escapeHTML(duration)))
	}
	if strings.TrimSpace(videoSrc) != "" {
		metaParts = append(metaParts, fmt.Sprintf(`<a class="restored-file-link" href="%s" target="_blank" rel="noopener noreferrer">打开源文件</a>`, escapeHTML(videoSrc)))
	}
	metaHTML := ""
	if len(metaParts) > 0 {
		metaHTML = fmt.Sprintf(`<div class="restored-file-meta">%s</div>`, strings.Join(metaParts, ""))
	}

	return strings.TrimSpace(fmt.Sprintf(`
<div class="block restored-file-block" data-block-id="%s" data-block-type="%s">
  <figure class="restored-file restored-file-video"%s>
    <div class="restored-file-preview">%s</div>
    <figcaption class="restored-file-caption">%s%s</figcaption>
  </figure>
</div>`, escapeHTML(expectedID), escapeHTML(blockType), figureStyle, previewHTML, titleHTML, metaHTML)), nil
}

func buildSheetFragment(ctx *buildContext, blockID string) (string, error) {
	block, ok := ctx.blocksByID[blockID]
	if !ok || block.Sheet == nil {
		return "", nil
	}
	sheet := block.Sheet
	if sheet.RowCount <= 0 || sheet.ColCount <= 0 {
		return "", nil
	}

	cellsByKey := map[string]StructuredSheetCell{}
	for _, cell := range sheet.Cells {
		cellsByKey[sheetCellKey(cell.Row, cell.Col)] = cell
	}

	spansByAnchor := map[string]StructuredSheetSpan{}
	coveredCells := map[string]struct{}{}
	spans := sheet.Spans
	if len(spans) == 0 {
		spans = inferSheetSpans(sheet, cellsByKey)
	}
	for _, span := range spans {
		if span.RowCount <= 0 || span.ColCount <= 0 {
			continue
		}
		key := sheetCellKey(span.Row, span.Col)
		spansByAnchor[key] = span
		for row := span.Row; row < span.Row+span.RowCount; row++ {
			for col := span.Col; col < span.Col+span.ColCount; col++ {
				if row == span.Row && col == span.Col {
					continue
				}
				coveredCells[sheetCellKey(row, col)] = struct{}{}
			}
		}
	}

	var body strings.Builder
	for row := 0; row < sheet.RowCount; row++ {
		body.WriteString("<tr>")
		for col := 0; col < sheet.ColCount; col++ {
			key := sheetCellKey(row, col)
			if _, covered := coveredCells[key]; covered {
				continue
			}

			cell := cellsByKey[key]
			span, hasSpan := spansByAnchor[key]
			tagName := "td"
			if isSheetHeaderCell(row, cell, hasSpan, sheet.ColCount) {
				tagName = "th"
			}

			body.WriteString("<")
			body.WriteString(tagName)
			body.WriteString(` class="restored-sheet-cell"`)
			if hasSpan && span.RowCount > 1 {
				body.WriteString(fmt.Sprintf(` rowspan="%d"`, span.RowCount))
			}
			if hasSpan && span.ColCount > 1 {
				body.WriteString(fmt.Sprintf(` colspan="%d"`, span.ColCount))
			}
			if styleAttr := buildSheetCellStyle(cell.Style); styleAttr != "" {
				body.WriteString(` style="`)
				body.WriteString(styleAttr)
				body.WriteString(`"`)
			}
			body.WriteString(">")
			body.WriteString(renderSheetCellContent(cell))
			body.WriteString("</")
			body.WriteString(tagName)
			body.WriteString(">")
		}
		body.WriteString("</tr>")
	}

	noteHTML := ""
	if sheet.Truncated {
		noteHTML = fmt.Sprintf(
			`<figcaption class="restored-sheet-note">表格已截取前 %d 行、%d 列，可在飞书原文中查看完整内容。</figcaption>`,
			sheet.RowCount,
			sheet.ColCount,
		)
	}

	return strings.TrimSpace(fmt.Sprintf(`
<div class="block restored-sheet-block" data-block-id="%s" data-block-type="sheet">
  <figure class="restored-sheet">
    <div class="restored-sheet-shell">
      <table class="restored-sheet-table">
        <tbody>%s</tbody>
      </table>
    </div>
    %s
  </figure>
</div>`, escapeHTML(blockID), body.String(), noteHTML)), nil
}

func buildCodeFragment(html, expectedID string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	root := doc.Find(fmt.Sprintf(`[data-block-id="%s"]`, expectedID)).First()
	if root.Length() == 0 {
		return "", nil
	}

	caption := extractCodeCaption(root)
	language := detectCodeLanguage(root)
	codeHTML := buildCodeBlockBody(root)
	if strings.TrimSpace(codeHTML) == "" {
		codeText := extractCodeText(root)
		if strings.TrimSpace(codeText) == "" {
			return "", nil
		}
		codeHTML = escapeHTML(codeText)
	}

	classAttr := "restored-code-content"
	if language != "" {
		classAttr += " language-" + sanitizeCodeLanguage(language)
	}

	headerHTML := ""
	if caption != "" || language != "" {
		captionHTML := ""
		languageHTML := ""
		if caption != "" {
			captionHTML = fmt.Sprintf(`<span class="restored-code-caption">%s</span>`, escapeHTML(caption))
		}
		if language != "" {
			languageHTML = fmt.Sprintf(`<span class="restored-code-language">%s</span>`, escapeHTML(language))
		}
		headerHTML = fmt.Sprintf(`<div class="restored-code-header"><div class="restored-code-header-main">%s</div>%s</div>`, captionHTML, languageHTML)
	}

	return strings.TrimSpace(fmt.Sprintf(`
<div class="block restored-code-block" data-block-id="%s" data-block-type="code">
  %s
  <pre class="restored-code-pre"><code class="%s">%s</code></pre>
</div>`, escapeHTML(expectedID), headerHTML, escapeHTML(classAttr), codeHTML)), nil
}

func extractCodeCaption(root *goquery.Selection) string {
	for _, selector := range []string{
		".code-block-caption-editor .ace-line",
		".code-block-caption .ace-line",
		".code-block-caption",
	} {
		value := strings.TrimSpace(strings.ReplaceAll(root.Find(selector).First().Text(), "\u200b", ""))
		if value != "" {
			return value
		}
	}
	return ""
}

func buildCodeBlockBody(root *goquery.Selection) string {
	lines := make([]string, 0, 16)
	lineRoot := root.Find(".code-block-zone-container").First()
	if lineRoot.Length() == 0 {
		lineRoot = root.Find(".code-block-content").First()
	}
	lineRoot.Find(".code-line-wrapper").Each(func(_ int, line *goquery.Selection) {
		lines = append(lines, renderCodeLine(line))
	})
	if len(lines) > 0 {
		return strings.Join(lines, "\n")
	}

	root.Find(".code-block-zone-container .ace-line, .code-block-content .ace-line").Each(func(_ int, line *goquery.Selection) {
		rendered := strings.TrimRight(strings.ReplaceAll(line.Text(), "\u200b", ""), "\n")
		lines = append(lines, escapeHTML(rendered))
	})
	if len(lines) > 0 {
		return strings.Join(lines, "\n")
	}
	return ""
}

func renderCodeLine(line *goquery.Selection) string {
	if line == nil || line.Length() == 0 {
		return ""
	}

	parts := make([]string, 0, line.Contents().Length())
	line.Contents().Each(func(_ int, child *goquery.Selection) {
		node := child.Get(0)
		if node == nil {
			return
		}
		switch node.Type {
		case xhtml.TextNode:
			text := strings.ReplaceAll(node.Data, "\u200b", "")
			if text != "" {
				parts = append(parts, escapeHTML(text))
			}
		case xhtml.ElementNode:
			classAttr, _ := child.Attr("class")
			if strings.Contains(classAttr, "code-block-fold-controller") {
				return
			}
			text := strings.ReplaceAll(child.Text(), "\u200b", "")
			if text == "" {
				return
			}
			if tokenClass := mapCodeTokenClass(classAttr); tokenClass != "" {
				parts = append(parts, fmt.Sprintf(`<span class="%s">%s</span>`, tokenClass, escapeHTML(text)))
				return
			}
			parts = append(parts, escapeHTML(text))
		}
	})
	return strings.Join(parts, "")
}

func mapCodeTokenClass(classAttr string) string {
	for _, className := range strings.Fields(classAttr) {
		if strings.HasPrefix(className, "code-hljs-") {
			token := sanitizeCodeLanguage(strings.TrimPrefix(className, "code-hljs-"))
			if token != "" {
				return "restored-code-token token-" + token
			}
		}
	}
	return ""
}

func buildVisualFragment(ctx *buildContext, blockID, blockType string) (string, error) {
	screenshotPath := filepath.Join(ctx.renderedImagesDir, blockID+".png")
	if _, err := os.Stat(screenshotPath); err != nil {
		return "", nil
	}
	src, err := inlineFileAsDataURI(ctx, screenshotPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(fmt.Sprintf(`
<div class="block restored-visual-block" data-block-id="%s" data-block-type="%s">
  <figure class="restored-image">
    <img src="%s" alt="区块 %s" loading="lazy" />
  </figure>
</div>`, escapeHTML(blockID), escapeHTML(blockType), escapeHTML(src), escapeHTML(blockID))), nil
}

func cleanupGenericFragment(ctx *buildContext, root *goquery.Selection) (string, error) {
	if root == nil || root.Length() == 0 {
		return "", nil
	}
	if err := prepareGenericSelection(ctx, root); err != nil {
		return "", err
	}
	return strings.ReplaceAll(selectionOuterHTML(root), "\u200b", ""), nil
}

func prepareGenericSelection(ctx *buildContext, root *goquery.Selection) error {
	for _, selector := range []string{
		`[data-block-type='grid'][data-block-id]`,
		`[data-block-type='image'][data-block-id]`,
		`[data-block-type='view'][data-block-id]`,
		`[data-block-type='file'][data-block-id]`,
		`[data-block-type='sheet'][data-block-id]`,
		`[data-block-type='code'][data-block-id]`,
		`[data-block-type='quote_container'][data-block-id]`,
	} {
		if err := replaceDescendants(ctx, root, selector); err != nil {
			return err
		}
	}

	for _, selector := range removeSelectors {
		root.Find(selector).Remove()
	}
	root.Find(".isEmpty").Each(func(_ int, selection *goquery.Selection) {
		if strings.TrimSpace(strings.ReplaceAll(selection.Text(), "\u200b", "")) == "" {
			selection.Remove()
		}
	})
	root.Find("a[href]").Each(func(_ int, selection *goquery.Selection) {
		selection.SetAttr("target", "_blank")
		selection.SetAttr("rel", "noopener noreferrer")
	})
	root.Find("img[src]").Each(func(_ int, selection *goquery.Selection) {
		src, _ := selection.Attr("src")
		if inlined, err := inlineLocalImageSrc(ctx, src); err == nil {
			selection.SetAttr("src", inlined)
		}
	})
	normalizeInlineSemantics(root)
	cleanupNodeAttributes(root)
	simplifyInlineWrappers(root)
	return nil
}

func normalizeInlineSemantics(root *goquery.Selection) {
	root.Find(".outer-u-container, .docx-outer-link-container").Each(func(_ int, selection *goquery.Selection) {
		link := selection.Find("a[href]").First()
		if link.Length() == 0 {
			return
		}
		normalizeAnchorText(link)
		if html, err := goquery.OuterHtml(link); err == nil {
			selection.ReplaceWithHtml(html)
		}
	})

	root.Find(".mention-doc-embed-container").Each(func(_ int, selection *goquery.Selection) {
		link := selection.Find("a[href]").First()
		if link.Length() == 0 {
			return
		}
		normalizeAnchorText(link)
		if html, err := goquery.OuterHtml(link); err == nil {
			selection.ReplaceWithHtml(html)
		}
	})

	root.Find(".inline-code").Each(func(_ int, selection *goquery.Selection) {
		content := strings.TrimSpace(strings.ReplaceAll(selection.Text(), "\u200b", ""))
		if content == "" {
			return
		}
		selection.ReplaceWithHtml("<code>" + escapeHTML(content) + "</code>")
	})

	root.Find(".text-highlight-background-yellow-light-bg").Each(func(_ int, selection *goquery.Selection) {
		content, err := selection.Html()
		if err != nil {
			return
		}
		content = strings.TrimSpace(strings.ReplaceAll(content, "\u200b", ""))
		if content == "" {
			content = escapeHTML(strings.TrimSpace(strings.ReplaceAll(selection.Text(), "\u200b", "")))
		}
		selection.ReplaceWithHtml("<mark>" + content + "</mark>")
	})

	root.Find(".textHighlight, .textHighlight-pink-text, .textHighlight-ccmtoken-doc-textcolor-red").Each(func(_ int, selection *goquery.Selection) {
		style, _ := selection.Attr("style")
		style = mergeInlineStyle(style, "color:#ef574d")
		if style != "" {
			selection.SetAttr("style", style)
		}
		selection.RemoveClass("textHighlight")
		selection.RemoveClass("textHighlight-pink-text")
		selection.RemoveClass("textHighlight-ccmtoken-doc-textcolor-red")
	})
}

func normalizeAnchorText(link *goquery.Selection) {
	linkText := strings.TrimSpace(strings.ReplaceAll(link.Find(".text, .embed-text-container").First().Text(), "\u200b", ""))
	if linkText == "" {
		linkText = strings.TrimSpace(strings.ReplaceAll(link.Text(), "\u200b", ""))
	}
	if linkText != "" {
		link.SetText(linkText)
	}
}

func firstAttr(root *goquery.Selection, selector string, attr string) string {
	if root == nil || root.Length() == 0 {
		return ""
	}
	value, _ := root.Find(selector).First().Attr(attr)
	return value
}

func mergeInlineStyle(existing string, addition string) string {
	existing = strings.TrimSpace(existing)
	addition = strings.TrimSpace(addition)
	switch {
	case existing == "":
		return addition
	case strings.Contains(strings.ToLower(existing), strings.ToLower(addition)):
		return existing
	default:
		return strings.TrimRight(existing, ";") + "; " + addition
	}
}

func bestFilePreviewSrc(ctx *buildContext, root *goquery.Selection, blockID string) (string, error) {
	screenshotPath := filepath.Join(ctx.renderedImagesDir, blockID+".png")
	if _, err := os.Stat(screenshotPath); err == nil {
		return inlineFileAsDataURI(ctx, screenshotPath)
	}
	if dataSrc, ok := root.Find("img.pip-overlayer-image").First().Attr("src"); ok && strings.HasPrefix(dataSrc, "data:") {
		return dataSrc, nil
	}
	return "", nil
}

func extractFileSummary(root *goquery.Selection) (string, string) {
	if root == nil || root.Length() == 0 {
		return "", ""
	}
	lines := extractFileLeafTexts(root)

	duration := ""
	for index := len(lines) - 1; index >= 0; index-- {
		if isDurationString(lines[index]) && lines[index] != "00:00" {
			duration = lines[index]
			break
		}
	}

	title := ""
	for _, line := range lines {
		if isLikelyFileName(line) {
			return line, duration
		}
	}
	for _, line := range lines {
		if line == duration || isDurationString(line) {
			continue
		}
		if strings.ContainsAny(line, ".-_") || strings.Contains(line, "Video") || strings.ContainsAny(line, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") {
			if len(line) > len(title) {
				title = line
			}
		}
	}
	return title, duration
}

func extractFileLeafTexts(root *goquery.Selection) []string {
	values := make([]string, 0, 12)
	seen := map[string]struct{}{}
	root.Find("*").Each(func(_ int, sel *goquery.Selection) {
		if sel.Children().Length() > 0 {
			return
		}
		value := strings.TrimSpace(strings.ReplaceAll(sel.Text(), "\u200b", ""))
		switch {
		case value == "", value == "/", value == "1x", value == "Unable to print":
			return
		case value == "360p" || value == "720p" || value == "1080p" || value == "2160p":
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		values = append(values, value)
	})
	return values
}

func isLikelyFileName(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, suffix := range []string{".mov", ".mp4", ".m4v", ".avi", ".mkv", ".webm"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func isDurationString(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 2 {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func replaceDescendants(ctx *buildContext, root *goquery.Selection, selector string) error {
	nodes := root.Find(selector).Nodes
	for index := len(nodes) - 1; index >= 0; index-- {
		selection := goquery.NewDocumentFromNode(nodes[index]).Selection
		blockID, _ := selection.Attr("data-block-id")
		if blockID == "" {
			continue
		}
		replacement, err := renderBlockFragment(ctx, selectionOuterHTML(selection), blockID)
		if err != nil {
			return err
		}
		selection.ReplaceWithHtml(replacement)
	}
	return nil
}

func cleanupNodeAttributes(root *goquery.Selection) {
	root.Find("*").Each(func(_ int, selection *goquery.Selection) {
		for _, node := range selection.Nodes {
			filtered := node.Attr[:0]
			for _, attr := range node.Attr {
				if _, strip := stripAttrs[attr.Key]; strip {
					continue
				}
				if strings.HasPrefix(attr.Key, "data-") {
					if _, keep := keepDataAttrs[attr.Key]; !keep {
						continue
					}
				}
				if attr.Key == "class" {
					attr.Val = sanitizeClassAttr(attr.Val)
					if attr.Val == "" {
						continue
					}
				}
				if attr.Key == "style" {
					attr.Val = sanitizeStyleAttr(attr.Val)
					if attr.Val == "" {
						continue
					}
				}
				filtered = append(filtered, attr)
			}
			node.Attr = filtered
		}
	})
}

func sanitizeClassAttr(value string) string {
	classes := strings.Fields(value)
	if len(classes) == 0 {
		return ""
	}
	kept := make([]string, 0, len(classes))
	for _, className := range classes {
		switch {
		case strings.HasPrefix(className, "restored-"):
			kept = append(kept, className)
		case strings.HasPrefix(className, "language-"):
			kept = append(kept, className)
		case className == "mention-doc",
			className == "link",
			className == "underline":
			kept = append(kept, className)
		}
	}
	return strings.Join(kept, " ")
}

func sanitizeStyleAttr(value string) string {
	parts := strings.Split(value, ";")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		segments := strings.SplitN(part, ":", 2)
		if len(segments) != 2 {
			continue
		}
		key := strings.TrimSpace(strings.ToLower(segments[0]))
		currentValue := strings.TrimSpace(segments[1])
		if currentValue == "" {
			continue
		}
		switch key {
		case "font-weight", "font-style", "text-decoration", "text-underline-offset", "color", "background", "background-color", "text-align":
			kept = append(kept, key+":"+currentValue)
		}
	}
	return strings.Join(kept, ";")
}

func simplifyInlineWrappers(root *goquery.Selection) {
	nodes := root.Find("span").Nodes
	for index := len(nodes) - 1; index >= 0; index-- {
		selection := goquery.NewDocumentFromNode(nodes[index]).Selection
		if len(selection.Nodes) == 0 || len(selection.Nodes[0].Attr) > 0 {
			continue
		}
		content, err := selection.Html()
		if err != nil {
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" {
			selection.Remove()
			continue
		}
		selection.ReplaceWithHtml(content)
	}
}

func loadTitle(ctx *buildContext) (string, error) {
	if ctx.reportPath != "" {
		if data, err := os.ReadFile(ctx.reportPath); err == nil {
			var payload struct {
				Title string `json:"title"`
			}
			if json.Unmarshal(data, &payload) == nil && payload.Title != "" {
				return payload.Title, nil
			}
		}
	}
	if data, err := os.ReadFile(ctx.metadataPath); err == nil {
		var payload struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Title != "" {
			return payload.Title, nil
		}
	}
	return "飞书文档正文", nil
}

func renderPageTemplate(title, bodyHTML string) string {
	safeTitle := escapeHTML(stripFeishuTitleSuffix(title))
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>%s - 正文还原版</title>
    <style>
      :root {
        color-scheme: light;
        --bg: #f5f7fb;
        --page: #ffffff;
        --text: #1f2329;
        --muted: #646a73;
        --border: #e3e8f0;
        --link: #1456f0;
        --ccmtoken-doc-blockbackground-red-solid: #f2b1aa;
        --ccmtoken-doc-highlightcolor-bg-red-soft: #fff5f4;
        --ccmtoken-doc-blockbackground-blue-solid: #b8cbff;
        --ccmtoken-doc-highlightcolor-bg-blue-soft: #f4f8ff;
      }
      * { box-sizing: border-box; }
      body {
        margin: 0;
        background: var(--bg);
        color: var(--text);
        font-family: "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
        line-height: 1.75;
      }
      a { color: var(--link); text-decoration: none; text-underline-offset: 0.14em; }
      a:hover { text-decoration: underline; }
      .doc-shell { max-width: 860px; margin: 0 auto; padding: 20px 12px 48px; }
      .doc-page { background: var(--page); padding: 24px 26px 36px; border: 1px solid rgba(20, 86, 240, 0.06); border-radius: 20px; box-shadow: 0 10px 34px rgba(15, 23, 42, 0.06); }
      .doc-page > .block:first-child { margin-top: 0; }
      .block { margin: 0 0 10px; }
      .text-editor, .caption-editor, .heading-content, .list-content, .page-block-content { min-width: 0; }
      .ace-line { white-space: pre-wrap; word-break: break-word; }
      .page-block-header { margin-bottom: 20px; padding-bottom: 16px; border-bottom: 1px solid rgba(31, 35, 41, 0.08); }
      .page-block-header .page-block-content { margin: 0; font-size: 39px; font-weight: 700; line-height: 1.35; letter-spacing: -0.02em; }
      .restored-text-block { margin: 0 0 10px; }
      .restored-text { margin: 0; font-size: 15.5px; line-height: 1.9; }
      .heading { display: flex; align-items: flex-start; gap: 8px; margin: 28px 0 8px; }
      .heading-order { color: var(--link); font-weight: 600; min-width: 34px; }
      .heading-h1 .ace-line { color: var(--link); font-size: 28px; font-weight: 700; line-height: 1.4; }
      .heading-h2 .ace-line { color: var(--link); font-size: 20px; font-weight: 700; line-height: 1.45; }
      .heading-h3 .ace-line { color: var(--link); font-size: 18px; font-weight: 700; line-height: 1.5; }
      .restored-heading-block { margin: 30px 0 12px; }
      .restored-heading { display: flex; align-items: baseline; gap: 10px; margin: 0; color: var(--link); }
      .restored-heading-order { flex: 0 0 auto; font-weight: 700; }
      .restored-heading-text { min-width: 0; color: var(--text); }
      .restored-heading-level-2 { font-size: 28px; line-height: 1.4; }
      .restored-heading-level-3 { font-size: 20px; line-height: 1.45; }
      .restored-heading-level-4 { font-size: 18px; line-height: 1.5; }
      .restored-heading-level-5 { font-size: 16px; line-height: 1.55; }
      .restored-heading-level-6 { font-size: 15px; line-height: 1.6; }
      .restored-heading-children { margin: 12px 0 0 10px; padding-left: 16px; border-left: 2px solid rgba(20, 86, 240, 0.08); }
      .list { display: flex; align-items: flex-start; gap: 8px; }
      .order { min-width: 24px; color: var(--link); font-weight: 600; }
      .restored-ordered-list { margin: 10px 0 14px 1.5em; padding: 0; }
      .restored-list-item { margin: 0 0 8px; padding-left: 4px; }
      .restored-list-item > :first-child { margin-top: 0; }
      .restored-list-item > :last-child { margin-bottom: 0; }
      .restored-grid-track { display: flex; align-items: flex-start; gap: 18px; }
      .restored-grid-column { min-width: 0; flex: var(--column-grow, 1) 1 0; }
      .restored-grid-column > .block:last-child { margin-bottom: 0; }
      .doc-page img { cursor: zoom-in; }
	      .restored-image { width: min(100%%, var(--image-max-width, 100%%)); margin: 0 auto; }
	      .restored-image img { display: block; width: 100%%; height: auto; border-radius: 12px; box-shadow: 0 6px 18px rgba(15, 23, 42, 0.05); }
	      .restored-image figcaption { margin-top: 8px; color: var(--muted); font-size: 14px; text-align: center; }
	      .restored-file { width: min(100%%, var(--file-max-width, 100%%)); margin: 16px auto; border: 1px solid #d8deea; border-radius: 14px; overflow: hidden; background: linear-gradient(180deg, #f8fbff 0%%, #ffffff 100%%); box-shadow: 0 8px 20px rgba(15, 23, 42, 0.06); }
	      .restored-file-preview { background: #0f1724; }
	      .restored-file-poster, .restored-file-video-player { display: block; width: 100%%; height: auto; max-height: 520px; object-fit: contain; }
	      .restored-file-video-player { background: #0f1724; aspect-ratio: 16 / 9; }
	      .restored-file-placeholder { display: flex; align-items: center; justify-content: center; min-height: 160px; color: #dbe7ff; font-size: 14px; letter-spacing: 0.04em; }
	      .restored-file-caption { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 14px; background: rgba(255, 255, 255, 0.92); }
	      .restored-file-name { min-width: 0; font-weight: 600; color: #24324a; word-break: break-word; }
	      .restored-file-meta { display: flex; align-items: center; gap: 10px; flex: 0 0 auto; }
	      .restored-file-duration { padding: 3px 8px; border-radius: 999px; background: rgba(36, 50, 74, 0.08); color: #42526b; font-size: 12px; font-weight: 600; }
	      .restored-file-link { font-size: 13px; font-weight: 600; color: #336df4; }
	      .restored-code-block { margin: 18px 0; border: 1px solid #d8deea; border-radius: 14px; overflow: hidden; background: linear-gradient(180deg, #f8fbff 0%%, #f2f6fc 100%%); box-shadow: 0 8px 22px rgba(15, 23, 42, 0.06); }
	      .restored-code-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 14px; background: rgba(225, 234, 255, 0.7); border-bottom: 1px solid #d8deea; }
	      .restored-code-header-main { min-width: 0; }
	      .restored-code-caption { display: inline-block; font-size: 12px; letter-spacing: 0.08em; text-transform: uppercase; color: #546072; }
	      .restored-code-language { flex: 0 0 auto; padding: 3px 10px; border-radius: 999px; background: rgba(84, 96, 114, 0.12); color: #334155; font-size: 12px; font-weight: 600; letter-spacing: 0.03em; text-transform: uppercase; }
	      .restored-code-pre { margin: 0; padding: 16px 18px; overflow-x: auto; background: #0f1724; color: #e8eefc; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace; font-size: 13px; line-height: 1.65; }
	      .restored-code-pre code,
	      .restored-code-pre .restored-code-content { display: block; padding: 0; border-radius: 0; background: transparent; color: inherit; font-family: inherit; font-size: inherit; white-space: pre; }
	      .restored-code-content .restored-code-token { background: transparent; }
	      .restored-code-token.token-comment { color: #7dd3fc; }
	      .restored-code-token.token-string { color: #c4f1be; }
	      .restored-code-token.token-attr { color: #fbbf24; }
	      .restored-code-token.token-keyword { color: #f472b6; }
	      .restored-code-token.token-number { color: #f59e0b; }
	      .restored-code-token.token-literal { color: #fda4af; }
	      .restored-code-token.token-built_in { color: #93c5fd; }
	      .restored-code-token.token-title { color: #c084fc; }
	      .restored-code-token.token-tag { color: #fca5a5; }
	      .restored-sheet { margin: 0; }
	      .restored-sheet-shell { overflow-x: auto; border: 1px solid var(--border); border-radius: 14px; background: linear-gradient(180deg, #fbfcff 0%%, #ffffff 100%%); box-shadow: 0 8px 22px rgba(15, 23, 42, 0.05); }
	      .restored-sheet-table { width: 100%%; border-collapse: separate; border-spacing: 0; table-layout: fixed; min-width: max-content; background: #ffffff; }
	      .restored-sheet-cell { min-width: 88px; padding: 10px 12px; border-right: 1px solid var(--border); border-bottom: 1px solid var(--border); vertical-align: top; font-size: 14px; line-height: 1.55; white-space: pre-wrap; word-break: break-word; }
	      .restored-sheet-table tr:last-child .restored-sheet-cell { border-bottom: 0; }
	      .restored-sheet-table .restored-sheet-cell:last-child { border-right: 0; }
	      .restored-sheet-table th.restored-sheet-cell { background: #f6f8fc; color: #1a2250; font-weight: 700; }
	      .restored-sheet-note { margin-top: 8px; color: var(--muted); font-size: 13px; text-align: right; }
	      .restored-sheet-empty { display: inline-block; min-width: 0.6em; }
	      .docx-callout-block, .docx-callout-render-unit { border-radius: 12px; }
      .callout-render-unit { padding: 0 !important; border: 0 !important; background: transparent !important; }
      .callout-block { display: flex; align-items: flex-start; gap: 10px; border: 1px solid var(--ccmtoken-doc-blockbackground-red-solid); border-radius: 10px; padding: 12px 16px; }
      .callout-block-children { flex: 1; }
      .callout-emoji-container { flex: 0 0 auto; margin-top: 1px; }
      .restored-callout-shell { display: flex; align-items: flex-start; gap: 12px; padding: 14px 16px; border: 1px solid var(--ccmtoken-doc-blockbackground-blue-solid); border-radius: 14px; background: linear-gradient(180deg, var(--ccmtoken-doc-highlightcolor-bg-blue-soft) 0%%, #ffffff 100%%); box-shadow: 0 4px 14px rgba(15, 23, 42, 0.04); }
      .restored-callout-emoji { flex: 0 0 auto; font-size: 20px; line-height: 1.2; }
      .restored-callout-body { min-width: 0; flex: 1; }
      .restored-callout-body > :first-child { margin-top: 0; }
      .restored-callout-body > :last-child { margin-bottom: 0; }
      .restored-quote-block { margin: 16px 0; padding: 0; border-left: 4px solid #c6d4f8; border-radius: 0 14px 14px 0; background: linear-gradient(180deg, #f8fbff 0%%, #ffffff 100%%); }
      .restored-quote-body { padding: 12px 16px 12px 18px; color: var(--text); }
      .restored-quote-body > :first-child { margin-top: 0; }
      .restored-quote-body > :last-child { margin-bottom: 0; }
      mark, .text-highlight-background-yellow-light-bg { background: #fff2a8; border-radius: 4px; padding: 0 0.1em; }
      .restored-divider { height: 0; border: 0; border-top: 1px solid var(--border); margin: 24px 0 28px; }
      code, .inline-code { padding: 0.08em 0.4em; border-radius: 6px; background: #f2f4f8; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 0.92em; }
      .mention-doc, .embed-inline-link { display: inline-flex; align-items: center; gap: 6px; }
      .mention-doc .text, .embed-inline-link .text { word-break: break-word; }
      .universe-icon, .custom-icon, svg { vertical-align: middle; }
      .image-lightbox[hidden] { display: none; }
      .image-lightbox {
        position: fixed;
        inset: 0;
        z-index: 9999;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 28px;
        background: rgba(15, 23, 42, 0.78);
        backdrop-filter: blur(6px);
      }
      .image-lightbox-backdrop {
        position: absolute;
        inset: 0;
        border: 0;
        padding: 0;
        background: transparent;
        cursor: zoom-out;
      }
      .image-lightbox-frame {
        position: relative;
        z-index: 1;
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 12px;
        max-width: min(96vw, 1440px);
        max-height: 92vh;
      }
      .image-lightbox-image {
        display: block;
        max-width: min(96vw, 1440px);
        max-height: calc(92vh - 72px);
        width: auto;
        height: auto;
        border-radius: 14px;
        box-shadow: 0 20px 60px rgba(15, 23, 42, 0.32);
        background: #ffffff;
      }
      .image-lightbox-caption {
        max-width: min(92vw, 960px);
        color: #f8fafc;
        font-size: 14px;
        line-height: 1.6;
        text-align: center;
      }
      .image-lightbox-close {
        position: absolute;
        top: -8px;
        right: -8px;
        width: 40px;
        height: 40px;
        border: 0;
        border-radius: 999px;
        background: rgba(255, 255, 255, 0.94);
        color: #1f2329;
        font-size: 24px;
        line-height: 1;
        cursor: pointer;
        box-shadow: 0 10px 24px rgba(15, 23, 42, 0.18);
      }
      @media (max-width: 860px) { .doc-shell { padding: 12px 8px 32px; } .doc-page { padding: 18px 16px 28px; border-radius: 16px; } .page-block-header .page-block-content { font-size: 34px; } }
      @media (max-width: 520px) { .restored-grid-track { flex-direction: column; gap: 12px; } .restored-file-caption { flex-direction: column; align-items: flex-start; } .page-block-header .page-block-content { font-size: 28px; } .image-lightbox { padding: 14px; } .image-lightbox-close { top: -2px; right: -2px; } }
    </style>
  </head>
  <body>
    <main class="doc-shell">
      <article class="doc-page">
        <header class="page-block-header">
          <h1 class="page-block-content">%s</h1>
        </header>
        %s
      </article>
    </main>
    <div class="image-lightbox" id="image-lightbox" hidden aria-hidden="true">
      <button class="image-lightbox-backdrop" type="button" aria-label="关闭图片放大层"></button>
      <div class="image-lightbox-frame" role="dialog" aria-modal="true" aria-label="图片放大预览">
        <button class="image-lightbox-close" type="button" aria-label="关闭图片放大层">&times;</button>
        <img class="image-lightbox-image" alt="" />
        <div class="image-lightbox-caption" hidden></div>
      </div>
    </div>
    <script>
      (() => {
        const lightbox = document.getElementById("image-lightbox");
        if (!lightbox) return;
        const lightboxImage = lightbox.querySelector(".image-lightbox-image");
        const lightboxCaption = lightbox.querySelector(".image-lightbox-caption");
        const closeButton = lightbox.querySelector(".image-lightbox-close");
        const backdrop = lightbox.querySelector(".image-lightbox-backdrop");
        let lastActiveImage = null;

        const getCaption = (img) => {
          const figure = img.closest("figure");
          const figcaption = figure ? figure.querySelector("figcaption") : null;
          const figcaptionText = figcaption ? figcaption.textContent.trim() : "";
          const altText = (img.getAttribute("alt") || "").trim();
          return figcaptionText || altText;
        };

        const closeLightbox = () => {
          lightbox.hidden = true;
          lightbox.setAttribute("aria-hidden", "true");
          document.body.style.overflow = "";
          lightboxImage.removeAttribute("src");
          lightboxImage.alt = "";
          lightboxCaption.textContent = "";
          lightboxCaption.hidden = true;
          if (lastActiveImage && typeof lastActiveImage.focus === "function") {
            lastActiveImage.focus({ preventScroll: true });
          }
        };

        const openLightbox = (img) => {
          const src = img.currentSrc || img.getAttribute("src");
          if (!src) return;
          lastActiveImage = img;
          lightboxImage.src = src;
          lightboxImage.alt = img.getAttribute("alt") || "";
          const caption = getCaption(img);
          if (caption) {
            lightboxCaption.textContent = caption;
            lightboxCaption.hidden = false;
          } else {
            lightboxCaption.textContent = "";
            lightboxCaption.hidden = true;
          }
          lightbox.hidden = false;
          lightbox.setAttribute("aria-hidden", "false");
          document.body.style.overflow = "hidden";
          closeButton.focus({ preventScroll: true });
        };

        document.addEventListener("click", (event) => {
          const target = event.target;
          if (!(target instanceof HTMLImageElement)) return;
          if (target.closest(".image-lightbox")) return;
          if (!target.currentSrc && !target.getAttribute("src")) return;
          if ((target.naturalWidth || 0) < 48 && (target.naturalHeight || 0) < 48) return;
          event.preventDefault();
          openLightbox(target);
        });

        lightbox.addEventListener("click", (event) => {
          if (event.target === lightboxImage) return;
          if (event.target === closeButton || event.target === backdrop) {
            closeLightbox();
          }
        });

        document.addEventListener("keydown", (event) => {
          if (!lightbox.hidden && event.key === "Escape") {
            event.preventDefault();
            closeLightbox();
          }
        });
      })();
    </script>
  </body>
</html>`, safeTitle, safeTitle, bodyHTML)
}

func inlineFileAsDataURI(ctx *buildContext, filePath string) (string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}
	if cached, ok := ctx.inlineCache[absPath]; ok {
		return cached, nil
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	contentType := mime.TypeByExtension(filepath.Ext(absPath))
	if contentType == "" {
		contentType = "image/png"
	}
	value := "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
	ctx.inlineCache[absPath] = value
	return value, nil
}

func inlineLocalImageSrc(ctx *buildContext, src string) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" || strings.HasPrefix(src, "data:") || strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "blob:") || strings.HasPrefix(src, "cid:") {
		return src, nil
	}
	imagePath := src
	if !filepath.IsAbs(imagePath) {
		imagePath = filepath.Join(ctx.rootDir, src)
	}
	if _, err := os.Stat(imagePath); err != nil {
		return src, nil
	}
	return inlineFileAsDataURI(ctx, imagePath)
}

func bestImageSrcForBlock(ctx *buildContext, blockID, fallbackSrc string) (string, error) {
	if assetPath, ok := ctx.assetMap[blockID]; ok {
		return inlineFileAsDataURI(ctx, assetPath)
	}
	screenshotPath := filepath.Join(ctx.renderedImagesDir, blockID+".png")
	if _, err := os.Stat(screenshotPath); err == nil {
		return inlineFileAsDataURI(ctx, screenshotPath)
	}
	if strings.HasPrefix(fallbackSrc, "blob:") {
		return "", nil
	}
	return fallbackSrc, nil
}

func parseWidthPX(style string) float64 {
	match := widthPxPattern.FindStringSubmatch(style)
	if len(match) < 2 {
		return 0
	}
	value, _ := strconv.ParseFloat(match[1], 64)
	return value
}

func parseGridPercent(style string) float64 {
	match := gridPercentPattern.FindStringSubmatch(style)
	if len(match) < 2 {
		return 0
	}
	value, _ := strconv.ParseFloat(match[1], 64)
	return value
}

func extractTextAlign(style string) string {
	for _, part := range strings.Split(style, ";") {
		segments := strings.SplitN(part, ":", 2)
		if len(segments) != 2 {
			continue
		}
		if strings.TrimSpace(strings.ToLower(segments[0])) != "text-align" {
			continue
		}
		align := strings.TrimSpace(strings.ToLower(segments[1]))
		switch align {
		case "left", "center", "right", "justify":
			return align
		}
	}
	return ""
}

func sheetCellKey(row, col int) string {
	return fmt.Sprintf("%d:%d", row, col)
}

func renderSheetCellContent(cell StructuredSheetCell) string {
	content := cell.Text
	if strings.TrimSpace(content) == "" {
		content = cell.Value
	}
	if strings.TrimSpace(content) == "" {
		return `<span class="restored-sheet-empty">&nbsp;</span>`
	}
	return strings.ReplaceAll(escapeHTML(content), "\n", "<br />")
}

func isSheetHeaderCell(row int, cell StructuredSheetCell, hasSpan bool, colCount int) bool {
	if row == 0 {
		return true
	}
	if hasSpan && strings.Contains(strings.ToLower(cell.Style.Font), "bold") {
		return true
	}
	if strings.Contains(strings.ToLower(cell.Style.Font), "bold") && colCount > 1 && row <= 2 {
		return true
	}
	return false
}

func buildSheetCellStyle(style StructuredSheetCellStyle) string {
	parts := []string{}
	if color := strings.TrimSpace(style.BackColor); color != "" && !strings.EqualFold(color, "#ffffff") {
		parts = append(parts, "background:"+color)
	}
	switch style.HAlign {
	case 1:
		parts = append(parts, "text-align:center")
	case 2:
		parts = append(parts, "text-align:right")
	}
	switch style.VAlign {
	case 1:
		parts = append(parts, "vertical-align:middle")
	case 2:
		parts = append(parts, "vertical-align:bottom")
	}
	if strings.Contains(strings.ToLower(style.Font), "bold") {
		parts = append(parts, "font-weight:700")
	}
	if style.WordWrap > 0 {
		parts = append(parts, "white-space:pre-wrap")
	}
	if style.TextDecoration > 0 {
		parts = append(parts, "text-decoration:underline")
	}
	return strings.Join(parts, ";")
}

func inferSheetSpans(sheet *StructuredSheetData, cellsByKey map[string]StructuredSheetCell) []StructuredSheetSpan {
	spans := []StructuredSheetSpan{}

	for row := 0; row < sheet.RowCount; row++ {
		nonEmptyCols := []int{}
		for col := 0; col < sheet.ColCount; col++ {
			if cellHasVisibleContent(cellsByKey[sheetCellKey(row, col)]) {
				nonEmptyCols = append(nonEmptyCols, col)
			}
		}
		if len(nonEmptyCols) == 1 {
			col := nonEmptyCols[0]
			if col == 0 && sheet.ColCount > 1 {
				spans = append(spans, StructuredSheetSpan{Row: row, Col: col, RowCount: 1, ColCount: sheet.ColCount})
			}
		}
	}

	for col := 0; col < sheet.ColCount; col++ {
		for row := 0; row < sheet.RowCount-1; row++ {
			cell := cellsByKey[sheetCellKey(row, col)]
			if !cellHasVisibleContent(cell) || !strings.Contains(strings.ToLower(cell.Style.Font), "bold") {
				continue
			}
			blankRows := 0
			for nextRow := row + 1; nextRow < sheet.RowCount; nextRow++ {
				nextCell := cellsByKey[sheetCellKey(nextRow, col)]
				if cellHasVisibleContent(nextCell) {
					break
				}
				blankRows++
			}
			if blankRows > 0 {
				spans = append(spans, StructuredSheetSpan{Row: row, Col: col, RowCount: blankRows + 1, ColCount: 1})
				row += blankRows
			}
		}
	}

	return spans
}

func cellHasVisibleContent(cell StructuredSheetCell) bool {
	return strings.TrimSpace(cell.Text) != "" || strings.TrimSpace(cell.Value) != ""
}

func detectCodeLanguage(root *goquery.Selection) string {
	candidates := []string{}
	if value, ok := root.Attr("data-language"); ok {
		candidates = append(candidates, value)
	}
	root.Find("[data-language]").Each(func(_ int, sel *goquery.Selection) {
		if value, ok := sel.Attr("data-language"); ok {
			candidates = append(candidates, value)
		}
	})
	root.Find("[class]").Each(func(_ int, sel *goquery.Selection) {
		if classAttr, ok := sel.Attr("class"); ok {
			for _, className := range strings.Fields(classAttr) {
				if strings.HasPrefix(className, "language-") {
					candidates = append(candidates, strings.TrimPrefix(className, "language-"))
				}
				if strings.HasPrefix(className, "lang-") {
					candidates = append(candidates, strings.TrimPrefix(className, "lang-"))
				}
			}
		}
	})
	for _, selector := range []string{
		".code-block-header-toolbar .code-block-header-btn span",
		".code-block-header-btn span",
		".code-block-language",
		".code-language",
		".block-code-language",
	} {
		root.Find(selector).Each(func(_ int, sel *goquery.Selection) {
			candidates = append(candidates, strings.TrimSpace(sel.Text()))
		})
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func extractCodeText(root *goquery.Selection) string {
	lines := []string{}
	root.Find(".code-block-zone-container .ace-line, .code-block-content .ace-line").Each(func(_ int, sel *goquery.Selection) {
		line := strings.TrimRight(strings.ReplaceAll(sel.Text(), "\u200b", ""), "\n")
		lines = append(lines, line)
	})
	if len(lines) > 0 {
		return strings.TrimRight(strings.Join(lines, "\n"), "\n")
	}
	if pre := root.Find("pre").First(); pre.Length() > 0 {
		return strings.TrimSpace(strings.ReplaceAll(pre.Text(), "\u200b", ""))
	}
	if code := root.Find("code").First(); code.Length() > 0 {
		return strings.TrimSpace(strings.ReplaceAll(code.Text(), "\u200b", ""))
	}
	return strings.TrimSpace(strings.ReplaceAll(root.Text(), "\u200b", ""))
}

func sanitizeCodeLanguage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	filtered := strings.Builder{}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '+' || r == '#' {
			filtered.WriteRune(r)
		}
	}
	return filtered.String()
}

func escapeHTML(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	return value
}

func selectionOuterHTML(selection *goquery.Selection) string {
	html, err := goquery.OuterHtml(selection)
	if err != nil {
		return ""
	}
	return html
}
