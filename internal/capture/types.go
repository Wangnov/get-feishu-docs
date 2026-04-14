package capture

type StructuredBlockImage struct {
	Src    string  `json:"src"`
	Alt    string  `json:"alt"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type StructuredSheetCellStyle struct {
	BackColor      string `json:"backColor,omitempty"`
	Font           string `json:"font,omitempty"`
	HAlign         int    `json:"hAlign,omitempty"`
	VAlign         int    `json:"vAlign,omitempty"`
	WordWrap       int    `json:"wordWrap,omitempty"`
	TextDecoration int    `json:"textDecoration,omitempty"`
}

type StructuredSheetCell struct {
	Row   int                      `json:"row"`
	Col   int                      `json:"col"`
	Text  string                   `json:"text,omitempty"`
	Value string                   `json:"value,omitempty"`
	Style StructuredSheetCellStyle `json:"style,omitempty"`
}

type StructuredSheetSpan struct {
	Row      int `json:"row"`
	Col      int `json:"col"`
	RowCount int `json:"rowCount"`
	ColCount int `json:"colCount"`
}

type StructuredSheetData struct {
	SheetID        string                `json:"sheetId,omitempty"`
	RowCount       int                   `json:"rowCount"`
	ColCount       int                   `json:"colCount"`
	SourceRowCount int                   `json:"sourceRowCount,omitempty"`
	SourceColCount int                   `json:"sourceColCount,omitempty"`
	Truncated      bool                  `json:"truncated,omitempty"`
	Cells          []StructuredSheetCell `json:"cells,omitempty"`
	Spans          []StructuredSheetSpan `json:"spans,omitempty"`
}

type StructuredBlockRecord struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	RecordID string                 `json:"recordId"`
	Text     string                 `json:"text"`
	HTML     string                 `json:"html"`
	Images   []StructuredBlockImage `json:"images"`
	Sheet    *StructuredSheetData   `json:"sheet,omitempty"`
}

type StructuredDocumentResult struct {
	BlocksJSONPath          string
	BlocksDir               string
	RenderedImagesDir       string
	AssetsDir               string
	TotalBlocks             int
	CapturedAssetCount      int
	CapturedScreenshotCount int
	MaxVisibleBlocks        int
}

type CaptureMetadata struct {
	URL                   string   `json:"url"`
	FinalURL              string   `json:"finalUrl"`
	Title                 string   `json:"title"`
	CapturedAt            string   `json:"capturedAt"`
	ReadySelector         string   `json:"readySelector"`
	UsedPassword          bool     `json:"usedPassword"`
	BodyOnlyHideSelectors []string `json:"bodyOnlyHideSelectors"`
}
