package capture

import (
	"time"

	"github.com/go-rod/rod"
)

var bodyOnlyHideSelectors = []string{
	".navigation-bar-wrapper",
	".doc-cover-wrapper",
	".catalogue-container",
	"#docCommentContainer",
	".docx-comment__first-comment-btn",
	".docx-global-comment",
	".help-block.group-btns",
	".copilot-chat-box-sidebar",
	".ccm-ask-ai-mount-container",
	".docx-comment-image-viewer-slot",
	".docx-image-viewer-slot",
	".docx-reminder",
	".docx-comment-numbers",
	".copilot-chat-box-mountpoint",
}

func buildBodyOnlyCSS() string {
	css := ""
	for _, sel := range bodyOnlyHideSelectors {
		css += sel + ","
	}
	css = css[:len(css)-1]
	return `
` + css + ` {
  display: none !important;
}

.page-main-item.editor {
  margin: 0 auto !important;
}`
}

func applyBodyOnlyPrune(page *rod.Page) error {
	if err := page.AddStyleTag(``, buildBodyOnlyCSS()); err != nil {
		// fall back to no-style path when style injection blocked.
	}

	_, _ = page.Eval(`
	(selectors) => {
		const sel = selectors.join(',');
		document.querySelectorAll(sel).forEach((node) => node.remove());
		const container = document.querySelector('.bear-web-x-container');
		if (container) {
			container.classList.remove('catalogue-opened');
		}
		return true;
	}
	`, bodyOnlyHideSelectors)

	_ = page.Timeout(2 * time.Second).WaitStable(1 * time.Second)
	time.Sleep(500 * time.Millisecond)
	return nil
}
