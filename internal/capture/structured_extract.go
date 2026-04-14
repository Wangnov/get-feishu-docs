package capture

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const blockSelector = "[data-block-id][data-block-type]"
const maxScrollIterations = 320

var scrollerCandidates = []string{".bear-web-x-container", ".page-main", ".page-main-item.editor"}
var screenshotFirstBlockTypes = map[string]struct{}{
	"file":  {},
	"sheet": {},
}

type scrollStatus struct {
	Found          bool   `json:"found"`
	ContainerLabel string `json:"containerLabel"`
	ScrollTop      int    `json:"scrollTop"`
	ClientHeight   int    `json:"clientHeight"`
	ScrollHeight   int    `json:"scrollHeight"`
	AtBottom       bool   `json:"atBottom"`
}

type dataURLPayload struct {
	MimeType string
	Buffer   []byte
}

func extractStructuredDocument(page *rod.Page, layout *outputLayout, progress func(string, string, map[string]int)) (*StructuredDocumentResult, error) {
	blocksByID := map[string]StructuredBlockRecord{}
	capturedAssets := map[string]struct{}{}
	capturedScreenshots := map[string]struct{}{}

	iterations := 0
	maxVisibleBlocks := 0

	for iterations < maxScrollIterations {
		if expanded, err := expandAllFoldedBlocks(page); err == nil && expanded > 0 {
			time.Sleep(300 * time.Millisecond)
		}

		visibleBlocks, err := snapshotVisibleBlocks(page)
		if err != nil {
			return nil, err
		}
		if len(visibleBlocks) > maxVisibleBlocks {
			maxVisibleBlocks = len(visibleBlocks)
		}

		for _, block := range visibleBlocks {
			if block.ID == "" {
				continue
			}
			blocksByID[block.ID] = mergeBlockRecord(blocksByID[block.ID], block)

			if block.Type == "sheet" && blocksByID[block.ID].Sheet == nil {
				sheetData, err := readBlockSheetData(page, block.ID)
				if err == nil && sheetData != nil {
					updated := blocksByID[block.ID]
					updated.Sheet = sheetData
					blocksByID[block.ID] = updated
				}
			}

			if block.Type == "image" {
				if _, done := capturedAssets[block.ID]; !done {
					dataURL, err := readBlockImageDataURL(page, block.ID)
					if err == nil && dataURL != "" {
						payload, err := decodeDataURL(dataURL)
						if err == nil {
							assetPath := filepath.Join(layout.assetsDir, block.ID+extensionForMimeType(payload.MimeType))
							if err := os.WriteFile(assetPath, payload.Buffer, 0o644); err == nil {
								capturedAssets[block.ID] = struct{}{}
							}
						}
					}
				}
				if _, ok := capturedAssets[block.ID]; !ok {
					if _, done := capturedScreenshots[block.ID]; !done {
						screenshotPath := filepath.Join(layout.renderedImagesDir, block.ID+".png")
						if err := captureBlockScreenshot(page, block.ID, screenshotPath); err == nil {
							capturedScreenshots[block.ID] = struct{}{}
						}
					}
				}
			}

			if _, needsScreenshot := screenshotFirstBlockTypes[block.Type]; needsScreenshot && blocksByID[block.ID].Sheet == nil {
				if _, done := capturedScreenshots[block.ID]; !done {
					screenshotPath := filepath.Join(layout.renderedImagesDir, block.ID+".png")
					if err := captureBlockScreenshot(page, block.ID, screenshotPath); err == nil {
						capturedScreenshots[block.ID] = struct{}{}
					}
				}
			}
		}

		status, err := currentScrollStatus(page)
		if err != nil {
			return nil, err
		}
		if !status.Found {
			return nil, errors.New("failed to determine the Feishu document scroll container")
		}
		if status.AtBottom {
			break
		}

		if iterations == 0 || iterations%8 == 0 {
			progress("extract", fmt.Sprintf("5/6 采集中：已看到 %d 个区块，已保存 %d 张图片资源。", len(blocksByID), len(capturedAssets)), map[string]int{
				"blocks":         len(blocksByID),
				"embeddedImages": len(capturedAssets),
			})
		}

		distance := status.ClientHeight * 78 / 100
		if distance < 360 {
			distance = 360
		}
		if err := scrollForward(page, distance); err != nil {
			return nil, err
		}
		time.Sleep(350 * time.Millisecond)
		iterations++
	}

	status, err := currentScrollStatus(page)
	if err != nil {
		return nil, err
	}
	if iterations >= maxScrollIterations && !status.AtBottom {
		return nil, errors.New("reached the document scroll safety limit before the end of the page")
	}

	blockList := make([]StructuredBlockRecord, 0, len(blocksByID))
	for _, block := range blocksByID {
		blockList = append(blockList, block)
	}
	sortBlockRecords(blockList)

	for _, block := range blockList {
		if err := os.WriteFile(filepath.Join(layout.blocksDir, block.ID+".html"), []byte(block.HTML), 0o644); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(layout.blocksJsonPath, mustJSON(blockList), 0o644); err != nil {
		return nil, err
	}

	progress("extract", fmt.Sprintf("5/6 采集完成：%d 个区块，%d 张原图资源，%d 个截图兜底块。", len(blockList), len(capturedAssets), len(capturedScreenshots)), map[string]int{
		"blocks":               len(blockList),
		"embeddedImages":       len(capturedAssets),
		"visualFallbackBlocks": len(capturedScreenshots),
	})

	return &StructuredDocumentResult{
		BlocksJSONPath:          layout.blocksJsonPath,
		BlocksDir:               layout.blocksDir,
		RenderedImagesDir:       layout.renderedImagesDir,
		AssetsDir:               layout.assetsDir,
		TotalBlocks:             len(blockList),
		CapturedAssetCount:      len(capturedAssets),
		CapturedScreenshotCount: len(capturedScreenshots),
		MaxVisibleBlocks:        maxVisibleBlocks,
	}, nil
}

func snapshotVisibleBlocks(page *rod.Page) ([]StructuredBlockRecord, error) {
	obj, err := page.Eval(`(selector) => {
		return Array.from(document.querySelectorAll(selector)).map((node) => {
			const images = Array.from(node.querySelectorAll("img")).map((image) => ({
				src: image.currentSrc || image.getAttribute("src") || "",
				alt: image.getAttribute("alt") || "",
				width: image.naturalWidth || Number.parseFloat(image.getAttribute("width") || "0") || 0,
				height: image.naturalHeight || Number.parseFloat(image.getAttribute("height") || "0") || 0
			}));
			return {
				id: node.getAttribute("data-block-id") || "",
				type: node.getAttribute("data-block-type") || "",
				recordId: node.getAttribute("data-record-id") || "",
				text: node instanceof HTMLElement ? (node.innerText || "") : (node.textContent || ""),
				html: node.outerHTML,
				images
			};
		});
	}`, blockSelector)
	if err != nil {
		return nil, err
	}
	blocks := []StructuredBlockRecord{}
	if err := obj.Value.Unmarshal(&blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

func readBlockImageDataURL(page *rod.Page, blockID string) (string, error) {
	obj, err := page.Evaluate(rod.Eval(`async ({ blockId }) => {
		const block = document.querySelector('[data-block-id="' + blockId + '"][data-block-type="image"]');
		if (!block) return "";

		const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
		const readCurrentDataURL = async () => {
			const image = block.querySelector("img");
			if (!image) return "";
			const src = image.currentSrc || image.getAttribute("src") || "";
			if (!src) return "";
			if (src.startsWith("data:")) return src;
			try {
				const response = await fetch(src, { credentials: "include" });
				if (!response.ok) return "";
				const blob = await response.blob();
				return await new Promise((resolve, reject) => {
					const reader = new FileReader();
					reader.onload = () => resolve(String(reader.result || ""));
					reader.onerror = () => reject(reader.error || new Error("Failed to read blob."));
					reader.readAsDataURL(blob);
				});
			} catch {
				return "";
			}
		};

		for (let attempt = 0; attempt < 3; attempt += 1) {
			const dataURL = await readCurrentDataURL();
			if (dataURL) return dataURL;

			const retry = block.querySelector('.retry-btn');
			if (retry instanceof HTMLElement) {
				retry.click();
			}
			await wait(300 * (attempt + 1));
		}
		return "";
	}`, map[string]any{"blockId": blockID}).ByPromise())
	if err != nil {
		return "", err
	}
	return obj.Value.Str(), nil
}

func readBlockSheetData(page *rod.Page, blockID string) (*StructuredSheetData, error) {
	obj, err := page.Evaluate(rod.Eval(`async ({ blockId, maxRows, maxCols, maxCells }) => {
		const block = document.querySelector('[data-block-id="' + blockId + '"][data-block-type="sheet"]');
		if (!block) return null;

		const wrapper = block.querySelector('.spreadsheet-wrap');
		const sheetClass = wrapper ? Array.from(wrapper.classList).find((name) => name.startsWith('sheet-id-')) : '';
		const sheetId = sheetClass ? sheetClass.slice('sheet-id-'.length) : '';
		const sheets = Array.isArray(window.spread?.sheets) ? window.spread.sheets : [];
		const sheet = sheets.find((item) => String(item?._id_) === sheetId || String(item?._name || item?.name) === sheetId) || null;
		if (!sheet || typeof sheet.getRowCount !== 'function' || typeof sheet.getColumnCount !== 'function') {
			return null;
		}

		const styleForCell = (row, col) => {
			try {
				const style = typeof sheet.getStyle === 'function' ? sheet.getStyle(row, col) : null;
				if (!style || typeof style !== 'object') return null;
				const out = {};
				if (typeof style.backColor === 'string' && style.backColor) out.backColor = style.backColor;
				if (typeof style.font === 'string' && style.font) out.font = style.font;
				if (typeof style.hAlign === 'number') out.hAlign = style.hAlign;
				if (typeof style.vAlign === 'number') out.vAlign = style.vAlign;
				if (typeof style.wordWrap === 'number') out.wordWrap = style.wordWrap;
				if (typeof style.textDecoration === 'number') out.textDecoration = style.textDecoration;
				return Object.keys(out).length > 0 ? out : null;
			} catch {
				return null;
			}
		};

		const safeString = (value) => {
			if (value == null) return '';
			return String(value);
		};

		const rawRowCount = Math.max(0, Number(sheet.getRowCount()) || 0);
		const rawColCount = Math.max(0, Number(sheet.getColumnCount()) || 0);
		let rowCount = Math.min(rawRowCount, maxRows);
		let colCount = Math.min(rawColCount, maxCols);
		let truncated = rowCount < rawRowCount || colCount < rawColCount;
		if (rowCount > 0 && colCount > 0 && rowCount * colCount > maxCells) {
			rowCount = Math.max(1, Math.min(rowCount, Math.floor(maxCells / Math.max(1, colCount))));
			truncated = true;
		}

		const cells = [];
		const spans = [];
		const seenSpans = new Set();
		let maxUsedRow = -1;
		let maxUsedCol = -1;

		for (let row = 0; row < rowCount; row += 1) {
			for (let col = 0; col < colCount; col += 1) {
				const text = safeString(typeof sheet.getText === 'function' ? sheet.getText(row, col) : '');
				const value = safeString(typeof sheet.getValue === 'function' ? sheet.getValue(row, col) : '');
				const style = styleForCell(row, col);
				const span = (() => {
					try {
						const raw = typeof sheet.getSpan === 'function' ? sheet.getSpan(row, col) : null;
						if (!raw || typeof raw !== 'object') return null;
						const normalized = {
							row: Number(raw.row) || 0,
							col: Number(raw.col) || 0,
							rowCount: Math.max(1, Number(raw.rowCount) || 1),
							colCount: Math.max(1, Number(raw.colCount) || 1)
						};
						if (normalized.row !== row || normalized.col !== col) return null;
						return normalized;
					} catch {
						return null;
					}
				})();

				if (span && (span.rowCount > 1 || span.colCount > 1)) {
					const key = [span.row, span.col, span.rowCount, span.colCount].join(':');
					if (!seenSpans.has(key)) {
						seenSpans.add(key);
						spans.push(span);
					}
					maxUsedRow = Math.max(maxUsedRow, span.row + span.rowCount - 1);
					maxUsedCol = Math.max(maxUsedCol, span.col + span.colCount - 1);
				}

				const hasStyledBackground = !!style?.backColor && style.backColor !== '#ffffff';
				const hasText = text !== '' || value !== '';
				const hasStyle = hasStyledBackground || !!style?.font || !!style?.hAlign || !!style?.vAlign || !!style?.textDecoration;
				if (hasText || hasStyle || span) {
					cells.push({
						row,
						col,
						text,
						value,
						style: style || undefined
					});
					maxUsedRow = Math.max(maxUsedRow, row);
					maxUsedCol = Math.max(maxUsedCol, col);
				}
			}
		}

		if (maxUsedRow < 0 || maxUsedCol < 0) {
			return null;
		}

		return {
			sheetId,
			rowCount: maxUsedRow + 1,
			colCount: maxUsedCol + 1,
			sourceRowCount: rawRowCount,
			sourceColCount: rawColCount,
			truncated,
			cells,
			spans
		};
	}`, map[string]any{
		"blockId":  blockID,
		"maxRows":  200,
		"maxCols":  50,
		"maxCells": 4000,
	}).ByPromise())
	if err != nil {
		return nil, err
	}
	if obj.Value.Nil() {
		return nil, nil
	}
	var sheet StructuredSheetData
	raw, err := json.Marshal(obj.Value.Raw())
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &sheet); err != nil {
		return nil, err
	}
	if sheet.RowCount <= 0 || sheet.ColCount <= 0 {
		return nil, nil
	}
	return &sheet, nil
}

func captureBlockScreenshot(page *rod.Page, blockID, outputPath string) error {
	element, err := page.Element(fmt.Sprintf(`[data-block-id="%s"]`, escapeCSSValue(blockID)))
	if err != nil {
		return err
	}
	bytes, err := element.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, bytes, 0o644)
}

func currentScrollStatus(page *rod.Page) (scrollStatus, error) {
	obj, err := page.Eval(`({ blockSelector, candidateSelectors }) => {
		const findScrollableAncestor = (element) => {
			let current = element instanceof HTMLElement ? element : element?.parentElement;
			while (current) {
				if (current.scrollHeight > current.clientHeight + 16) {
					return current;
				}
				current = current.parentElement;
			}
			return null;
		};

		const block = document.querySelector(blockSelector);
		const fromBlock = findScrollableAncestor(block);
		if (fromBlock) {
			return {
				found: true,
				containerLabel: fromBlock.className || fromBlock.tagName,
				scrollTop: fromBlock.scrollTop,
				clientHeight: fromBlock.clientHeight,
				scrollHeight: fromBlock.scrollHeight,
				atBottom: fromBlock.scrollTop + fromBlock.clientHeight >= fromBlock.scrollHeight - 2
			};
		}

		for (const selector of candidateSelectors) {
			const candidate = document.querySelector(selector);
			if (candidate instanceof HTMLElement && candidate.scrollHeight > candidate.clientHeight + 16) {
				return {
					found: true,
					containerLabel: selector,
					scrollTop: candidate.scrollTop,
					clientHeight: candidate.clientHeight,
					scrollHeight: candidate.scrollHeight,
					atBottom: candidate.scrollTop + candidate.clientHeight >= candidate.scrollHeight - 2
				};
			}
		}

		const pageScroller = document.scrollingElement;
		if (pageScroller && pageScroller.scrollHeight > pageScroller.clientHeight + 16) {
			return {
				found: true,
				containerLabel: "document.scrollingElement",
				scrollTop: pageScroller.scrollTop,
				clientHeight: pageScroller.clientHeight,
				scrollHeight: pageScroller.scrollHeight,
				atBottom: pageScroller.scrollTop + pageScroller.clientHeight >= pageScroller.scrollHeight - 2
			};
		}

		return {
			found: false,
			containerLabel: "",
			scrollTop: 0,
			clientHeight: 0,
			scrollHeight: 0,
			atBottom: false
		};
	}`, map[string]any{
		"blockSelector":      blockSelector,
		"candidateSelectors": scrollerCandidates,
	})
	if err != nil {
		return scrollStatus{}, err
	}
	var status scrollStatus
	if err := obj.Value.Unmarshal(&status); err != nil {
		return scrollStatus{}, err
	}
	return status, nil
}

func scrollForward(page *rod.Page, distance int) error {
	_, err := page.Eval(`({ blockSelector, candidateSelectors, distance }) => {
		const findScrollableAncestor = (element) => {
			let current = element instanceof HTMLElement ? element : element?.parentElement;
			while (current) {
				if (current.scrollHeight > current.clientHeight + 16) return current;
				current = current.parentElement;
			}
			return null;
		};

		const block = document.querySelector(blockSelector);
		const fromBlock = findScrollableAncestor(block);
		if (fromBlock) {
			fromBlock.scrollBy(0, distance);
			return true;
		}

		for (const selector of candidateSelectors) {
			const candidate = document.querySelector(selector);
			if (candidate instanceof HTMLElement && candidate.scrollHeight > candidate.clientHeight + 16) {
				candidate.scrollBy(0, distance);
				return true;
			}
		}

		if (document.scrollingElement) {
			document.scrollingElement.scrollBy(0, distance);
			return true;
		}

		return false;
	}`, map[string]any{
		"blockSelector":      blockSelector,
		"candidateSelectors": scrollerCandidates,
		"distance":           distance,
	})
	return err
}

func mergeBlockRecord(existing, next StructuredBlockRecord) StructuredBlockRecord {
	if existing.ID == "" {
		return next
	}
	merged := existing
	merged.Type = pickString(next.Type, existing.Type)
	merged.RecordID = pickString(next.RecordID, existing.RecordID)
	merged.Text = pickString(next.Text, existing.Text)
	if len(next.HTML) >= len(existing.HTML) {
		merged.HTML = next.HTML
	}
	if len(next.Images) > 0 {
		merged.Images = next.Images
	}
	if next.Sheet != nil {
		merged.Sheet = next.Sheet
	}
	return merged
}

func decodeDataURL(dataURL string) (dataURLPayload, error) {
	parts := strings.SplitN(dataURL, ",", 2)
	if len(parts) != 2 {
		return dataURLPayload{}, errors.New("unsupported data URL payload")
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

func extensionForMimeType(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	default:
		return ".png"
	}
}

func compareBlockIDs(left, right string) int {
	leftNumeric, leftErr := strconv.Atoi(left)
	rightNumeric, rightErr := strconv.Atoi(right)
	if leftErr == nil && rightErr == nil {
		switch {
		case leftNumeric < rightNumeric:
			return -1
		case leftNumeric > rightNumeric:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(left, right)
}

func sortBlockRecords(records []StructuredBlockRecord) {
	for i := 0; i < len(records); i++ {
		for j := i + 1; j < len(records); j++ {
			if compareBlockIDs(records[j].ID, records[i].ID) < 0 {
				records[i], records[j] = records[j], records[i]
			}
		}
	}
}

func escapeCSSValue(value string) string {
	return strings.ReplaceAll(value, `"`, `\"`)
}

func pickString(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func loadStructuredBlocks(path string) ([]StructuredBlockRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var blocks []StructuredBlockRecord
	if err := json.Unmarshal(data, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}
