package capture

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var windowsReservedBasename = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$`)

var unsafePathChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`)

func stripFeishuTitleSuffix(title string) string {
	value := strings.TrimSpace(title)
	patterns := []string{
		" - 飞书云文档",
		" - Feishu Docs",
	}
	for _, pattern := range patterns {
		if strings.HasSuffix(value, pattern) {
			value = strings.TrimSpace(strings.TrimSuffix(value, pattern))
		}
	}
	return value
}

func sanitizePathSegment(input string) string {
	value := strings.TrimSpace(input)
	value = norm.NFKC.String(value)
	value = stripFeishuTitleSuffix(value)
	value = unsafePathChars.ReplaceAllString(value, " ")
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), "-")
	value = strings.TrimRight(value, ". ")
	value = strings.Trim(value, "-.")
	if value == "" {
		value = "feishu-doc"
	}

	value = strings.ReplaceAll(value, "  ", " ")
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	value = strings.Trim(value, "- .")

	if windowsReservedBasename.MatchString(value) {
		value = "_" + value
	}

	if len(value) > 120 {
		value = strings.TrimRight(value[:120], ". -")
	}

	if value == "" {
		return "feishu-doc"
	}

	return value
}
