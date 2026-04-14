# get-feishu-docs

`get-feishu-docs` exports Feishu documents into stable local deliverables with one command.

It launches Chrome or Chromium, opens the document, handles password entry, expands folded content, captures structured blocks, and writes local outputs for people and agents.

## Features

- Human-first CLI: `get-feishu-docs <url>`
- Stable machine contract: `--json` and `--progress=jsonl`
- Browser strategy: `auto -> system Chrome/Chromium -> managed Chromium`
- Outputs: HTML by default, optional PNG, PDF, Markdown, unified assets, and `manifest.json`
- Localized videos in HTML export
- Click-to-zoom images in HTML export

## Install

### Download a release build

Download the archive for your platform from the GitHub Releases page, extract it, and run `get-feishu-docs`.

### Build from source

```bash
go build ./cmd/get-feishu-docs
```

## Usage

```bash
get-feishu-docs 'https://example.feishu.cn/docx/your-doc-id'
```

Export all formats:

```bash
get-feishu-docs 'https://example.feishu.cn/docx/your-doc-id' --output-all
```

Agent mode:

```bash
get-feishu-docs 'https://example.feishu.cn/docx/your-doc-id' \
  --password-stdin \
  --json \
  --progress=jsonl
```

Prepare the browser ahead of time:

```bash
get-feishu-docs doctor
get-feishu-docs browser install
```

## Output layout

Successful runs write a folder like:

```text
output/
  Document-Title/
    Document-Title.html
    Document-Title.png
    Document-Title.pdf
    Document-Title.md
    Document-Title-assets/
    manifest.json
```

## Repository notes

- `docs/spec/` contains tracked CLI specs.
- `docs/dev/` is ignored for local design work.
- `output/` and `output-compare/` are ignored to keep generated document data out of git.
