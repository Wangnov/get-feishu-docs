package capture

import (
	"fmt"
	"os"
	"path/filepath"
)

type outputLayout struct {
	baseOutDir              string
	safeBaseName            string
	rootDir                 string
	workingRoot             string
	debugEnabled            bool
	debugDir                string
	rawDir                  string
	bodyDir                 string
	dataDir                 string
	finalDir                string
	markdownAssetsDir       string
	replicaAssetsDir        string
	workingReplicaAssetsDir string
	manifestPath            string
	reportPath              string
	replicaHtmlPath         string
	replicaPngPath          string
	replicaPdfPath          string
	replicaMdPath           string
	workingReplicaHtmlPath  string
	rawPageHtmlPath         string
	rawPageMhtmlPath        string
	rawPagePngPath          string
	bodyHtmlPath            string
	bodySnapshotMhtmlPath   string
	bodyPreviewPngPath      string
	metadataPath            string
	blocksJsonPath          string
	blocksDir               string
	assetsDir               string
	renderedImagesDir       string
	cleanup                 func() error

	selectedOutputs []string
}

func createOutputLayout(outDir, title string, debug bool) (*outputLayout, error) {
	baseOutDir, err := filepath.Abs(outDir)
	if err != nil {
		baseOutDir = outDir
	}

	safeBaseName := sanitizePathSegment(title)
	rootDir := filepath.Join(baseOutDir, safeBaseName)
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, err
	}

	stalePaths := []string{
		filepath.Join(rootDir, "_debug"),
		filepath.Join(rootDir, fmt.Sprintf("%s.html", safeBaseName)),
		filepath.Join(rootDir, fmt.Sprintf("%s.png", safeBaseName)),
		filepath.Join(rootDir, fmt.Sprintf("%s.pdf", safeBaseName)),
		filepath.Join(rootDir, fmt.Sprintf("%s.md", safeBaseName)),
		filepath.Join(rootDir, fmt.Sprintf("%s.report.json", safeBaseName)),
		filepath.Join(rootDir, fmt.Sprintf("%s-assets", safeBaseName)),
		filepath.Join(rootDir, "manifest.json"),
		filepath.Join(rootDir, "snapshot.mhtml"),
		filepath.Join(rootDir, "preview.png"),
		filepath.Join(rootDir, "report.json"),
	}
	for _, stale := range stalePaths {
		_ = os.RemoveAll(stale)
	}

	workingRoot := rootDir
	debugDir := ""
	reportPath := ""
	cleanup := func() error { return nil }

	if debug {
		debugDir = filepath.Join(rootDir, "_debug")
		workingRoot = debugDir
		reportPath = filepath.Join(debugDir, fmt.Sprintf("%s.report.json", safeBaseName))
		if err := os.MkdirAll(debugDir, 0o755); err != nil {
			return nil, err
		}
	} else {
		workingRoot, err = os.MkdirTemp("", "feishu-docs-cli-")
		if err != nil {
			return nil, err
		}
		workingDir := workingRoot
		cleanup = func() error { return os.RemoveAll(workingDir) }
	}

	rawDir := filepath.Join(workingRoot, "raw")
	bodyDir := filepath.Join(workingRoot, "body")
	dataDir := filepath.Join(workingRoot, "data")
	finalDir := filepath.Join(workingRoot, "final")
	blocksDir := filepath.Join(dataDir, "blocks")
	assetsDir := filepath.Join(dataDir, "assets")
	renderedImagesDir := filepath.Join(dataDir, "rendered-images")
	workingReplicaAssetsDir := filepath.Join(finalDir, fmt.Sprintf("%s-assets", safeBaseName))

	paths := []string{rawDir, bodyDir, dataDir, finalDir, blocksDir, assetsDir, renderedImagesDir, workingReplicaAssetsDir}
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return nil, err
		}
	}

	markdownAssetsDir := filepath.Join(rootDir, fmt.Sprintf("%s-assets", safeBaseName))
	if debug {
		if err := os.MkdirAll(markdownAssetsDir, 0o755); err != nil {
			return nil, err
		}
	}

	return &outputLayout{
		baseOutDir:              baseOutDir,
		safeBaseName:            safeBaseName,
		rootDir:                 rootDir,
		workingRoot:             workingRoot,
		debugEnabled:            debug,
		debugDir:                debugDir,
		rawDir:                  rawDir,
		bodyDir:                 bodyDir,
		dataDir:                 dataDir,
		finalDir:                finalDir,
		markdownAssetsDir:       markdownAssetsDir,
		replicaAssetsDir:        markdownAssetsDir,
		workingReplicaAssetsDir: workingReplicaAssetsDir,
		manifestPath:            filepath.Join(rootDir, "manifest.json"),
		reportPath:              reportPath,
		replicaHtmlPath:         filepath.Join(rootDir, fmt.Sprintf("%s.html", safeBaseName)),
		replicaPngPath:          filepath.Join(rootDir, fmt.Sprintf("%s.png", safeBaseName)),
		replicaPdfPath:          filepath.Join(rootDir, fmt.Sprintf("%s.pdf", safeBaseName)),
		replicaMdPath:           filepath.Join(rootDir, fmt.Sprintf("%s.md", safeBaseName)),
		workingReplicaHtmlPath:  filepath.Join(finalDir, fmt.Sprintf("%s.html", safeBaseName)),
		rawPageHtmlPath:         filepath.Join(rawDir, "page.html"),
		rawPageMhtmlPath:        filepath.Join(rawDir, "page.mhtml"),
		rawPagePngPath:          filepath.Join(rawDir, "page.png"),
		bodyHtmlPath:            filepath.Join(bodyDir, "body.html"),
		bodySnapshotMhtmlPath:   filepath.Join(bodyDir, "snapshot.mhtml"),
		bodyPreviewPngPath:      filepath.Join(bodyDir, "preview.png"),
		metadataPath:            filepath.Join(dataDir, "metadata.json"),
		blocksJsonPath:          filepath.Join(dataDir, "blocks.json"),
		blocksDir:               filepath.Join(dataDir, "blocks"),
		assetsDir:               filepath.Join(dataDir, "assets"),
		renderedImagesDir:       filepath.Join(dataDir, "rendered-images"),
		cleanup:                 cleanup,
		selectedOutputs:         nil,
	}, nil
}
