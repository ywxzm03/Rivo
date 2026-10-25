package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"

	"github.com/disintegration/imaging"
	"golang.org/x/image/webp"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const (
	imageMaxWidth  = 2000
	imageMaxHeight = 2000
	imageMaxBytes  = 4*1024*1024 + 512*1024
)

var imageJPEGQualities = []int{85, 80, 70, 55, 40}

var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

var (
	errImageDecode = errors.New("[Image omitted: could not be converted to a supported inline image format.]")
	errImageTooBig = errors.New("[Image omitted: could not be resized below the inline image size limit.]")
)

const noVisionNote = "[Current model does not support images. The image will be omitted from this request.]"

type Read struct {
	SupportsImages bool
	WorkingDir     string
}

func (r *Read) Name() string { return "read" }

func (r *Read) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (r *Read) PromptSnippet() string { return "Read file contents" }

func (r *Read) PromptGuidelines() []string {
	return []string{"Use read to examine files instead of cat or sed."}
}

func (r *Read) Description() string {
	return fmt.Sprintf(
		"Read the contents of a file. Supports text files and images (jpg, png, webp). Images are sent as attachments. "+
			"For text files, output is truncated to %d lines or %dKB (whichever is hit first). "+
			"Use offset/limit for large files. When you need the full file, continue with offset until complete.",
		defaultMaxLines, defaultMaxBytes/1024,
	)
}

func (r *Read) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"path"},
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to read (relative or absolute)",
			},
			"offset": map[string]any{
				"type":        "number",
				"description": "Line number to start reading from (1-indexed)",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "Maximum number of lines to read",
			},
		},
	}
}

// Execute 读取文件：图片返回文字说明加图片附件，文本按行截断并附上续读提示
func (r *Read) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return llm.ToolResult{}, fmt.Errorf("read: missing required parameter: path")
	}
	offset, _ := argInt(args, "offset")
	limit, hasLimit := argInt(args, "limit")
	if limit <= 0 {
		hasLimit = false
	}

	// 1.解析路径后整读
	absPath, err := resolvePathFrom(path, r.WorkingDir)
	if err != nil {
		return llm.ToolResult{}, err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return llm.ToolResult{}, err
	}
	if len(data) == 0 {
		return llm.ToolResult{Content: textContents("")}, nil
	}

	// 2.是图片就走图片分支
	if mimeType := sniffImage(data); mimeType != "" {
		return llm.ToolResult{Content: r.readImage(mimeType, data)}, nil
	}

	// 3.切行，结尾换行切出的空串不算一行
	allLines := strings.Split(string(data), "\n")
	totalFileLines := len(allLines)
	if allLines[totalFileLines-1] == "" {
		totalFileLines--
	}

	startLine := 0
	if offset > 0 {
		startLine = offset - 1
	}
	startLineDisplay := startLine + 1
	if startLine >= totalFileLines {
		return llm.ToolResult{}, fmt.Errorf("Offset %d is beyond end of file (%d lines total)", offset, totalFileLines)
	}

	// 4.limit 只切出候选区间，真正的截断交给 truncateHead
	var selected string
	userLimitedLines := 0
	if hasLimit {
		userLimitedLines = min(limit, totalFileLines-startLine)
		selected = strings.Join(allLines[startLine:startLine+userLimitedLines], "\n")
	} else {
		selected = strings.Join(allLines[startLine:], "\n")
	}

	// 5.按截断结果拼续读提示，判定顺序不能换
	truncation := truncateHead(selected, defaultMaxLines, defaultMaxBytes)
	switch {
	case truncation.FirstLineExceedsLimit:
		return llm.ToolResult{Content: textContents(fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use offset=%d to continue.]",
			startLineDisplay, formatSize(len(allLines[startLine])), formatSize(defaultMaxBytes),
			startLineDisplay+1))}, nil
	case truncation.Truncated || (hasLimit && startLine+userLimitedLines < totalFileLines):
		endLineDisplay := startLineDisplay + truncation.OutputLines - 1
		reason := ""
		if truncation.TruncatedBy == truncatedByBytes {
			reason = formatSize(defaultMaxBytes) + " limit"
		}
		return llm.ToolResult{Content: textContents(truncation.Content + truncationNote(
			fmt.Sprintf("Showing lines %d-%d of %d", startLineDisplay, endLineDisplay, totalFileLines),
			reason,
			fmt.Sprintf("offset=%d", endLineDisplay+1),
		))}, nil
	default:
		return llm.ToolResult{Content: textContents(truncation.Content)}, nil
	}
}

func (r *Read) readImage(mimeType string, data []byte) []llm.Content {
	note := "Read image file [" + mimeType + "]"

	if !r.SupportsImages {
		return textContents(note + "\n" + noVisionNote)
	}

	result, err := processImage(data, mimeType, defaultImageLimits)
	if err != nil {
		return textContents(note + "\n" + err.Error())
	}
	if result.Resized {
		note += "\n" + dimensionNote(result)
	}
	return []llm.Content{
		llm.TextContent{Text: note},
		llm.ImageContent{Data: result.Data, MIMEType: result.MIMEType},
	}
}

// sniffImage 只看开头的魔数，返回 image/jpeg、image/png、image/webp 或空串
func sniffImage(data []byte) string {
	if len(data) >= 4 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff && data[3] != 0xf7 {
		return "image/jpeg"
	}
	if len(data) >= len(pngSignature) && bytes.HasPrefix(data, pngSignature) {
		return "image/png"
	}
	if len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp"
	}
	return ""
}

// SniffImage 暴露图片魔数识别，供剪贴板文件附件复用
func SniffImage(data []byte) string { return sniffImage(data) }

type imageResult struct {
	Data           string
	MIMEType       string
	Resized        bool
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
}

// ImageData 是经过统一尺寸和体积限制处理后的内联图片
type ImageData struct {
	Data           string
	MIMEType       string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
}

// ProcessImage 供非 read 工具路径复用图片处理规则
func ProcessImage(data []byte, mimeType string) (ImageData, error) {
	result, err := processImage(data, mimeType, defaultImageLimits)
	if err != nil {
		return ImageData{}, err
	}
	return ImageData{
		Data:           result.Data,
		MIMEType:       result.MIMEType,
		OriginalWidth:  result.OriginalWidth,
		OriginalHeight: result.OriginalHeight,
		Width:          result.Width,
		Height:         result.Height,
	}, nil
}

func decodeImage(data []byte, mimeType string) (image.Image, error) {
	reader := bytes.NewReader(data)
	switch mimeType {
	case "image/jpeg":
		return imaging.Decode(reader, imaging.AutoOrientation(true))
	case "image/png":
		return png.Decode(reader)
	case "image/webp":
		return webp.Decode(reader)
	}
	return nil, errImageDecode
}

type imageLimits struct {
	maxWidth  int
	maxHeight int
	maxBytes  int
}

var defaultImageLimits = imageLimits{
	maxWidth:  imageMaxWidth,
	maxHeight: imageMaxHeight,
	maxBytes:  imageMaxBytes,
}

// processImage 把图片压到内联限制内，返回 base64 与最终 MIME
func processImage(data []byte, mimeType string, limits imageLimits) (imageResult, error) {
	img, err := decodeImage(data, mimeType)
	if err != nil {
		return imageResult{}, errImageDecode
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	result := imageResult{
		MIMEType:       mimeType,
		OriginalWidth:  width,
		OriginalHeight: height,
		Width:          width,
		Height:         height,
	}

	// 1.已在限内就原样返回，不重新编码
	original := base64.StdEncoding.EncodeToString(data)
	if width <= limits.maxWidth && height <= limits.maxHeight && len(original) < limits.maxBytes {
		result.Data = original
		return result, nil
	}

	// 2.等比缩到尺寸上限内
	targetWidth, targetHeight := width, height
	if targetWidth > limits.maxWidth {
		targetHeight = targetHeight * limits.maxWidth / targetWidth
		targetWidth = limits.maxWidth
	}
	if targetHeight > limits.maxHeight {
		targetWidth = targetWidth * limits.maxHeight / targetHeight
		targetHeight = limits.maxHeight
	}

	// 3.逐步缩小，每轮先试 PNG 再试各档质量的 JPEG
	for {
		resized := imaging.Resize(img, targetWidth, targetHeight, imaging.Lanczos)
		for _, candidate := range encodeCandidates(resized) {
			if len(candidate.data) < limits.maxBytes {
				result.Data = candidate.data
				result.MIMEType = candidate.mimeType
				result.Resized = true
				result.Width = targetWidth
				result.Height = targetHeight
				return result, nil
			}
		}

		if targetWidth == 1 && targetHeight == 1 {
			break
		}
		nextWidth := max(1, targetWidth*3/4)
		nextHeight := max(1, targetHeight*3/4)
		if nextWidth == targetWidth && nextHeight == targetHeight {
			break
		}
		targetWidth, targetHeight = nextWidth, nextHeight
	}
	return imageResult{}, errImageTooBig
}

type encodedImage struct {
	data     string
	mimeType string
}

// encodeCandidates 依次产出 PNG 与各档质量的 JPEG，顺序决定优先选谁
func encodeCandidates(img image.Image) []encodedImage {
	out := make([]encodedImage, 0, 1+len(imageJPEGQualities))
	if data, err := encodeImage(img, imaging.PNG, 0); err == nil {
		out = append(out, encodedImage{data: data, mimeType: "image/png"})
	}
	for _, quality := range imageJPEGQualities {
		if data, err := encodeImage(img, imaging.JPEG, quality); err == nil {
			out = append(out, encodedImage{data: data, mimeType: "image/jpeg"})
		}
	}
	return out
}

func encodeImage(img image.Image, format imaging.Format, quality int) (string, error) {
	var buf bytes.Buffer
	var err error
	if format == imaging.JPEG {
		err = imaging.Encode(&buf, img, format, imaging.JPEGQuality(quality))
	} else {
		err = imaging.Encode(&buf, img, format)
	}
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// dimensionNote 告诉模型缩放前后的尺寸，方便把坐标映射回原图
func dimensionNote(result imageResult) string {
	scale := float64(result.OriginalWidth) / float64(result.Width)
	return fmt.Sprintf("[Image: original %dx%d, displayed at %dx%d. Multiply coordinates by %.2f to map to original image.]",
		result.OriginalWidth, result.OriginalHeight, result.Width, result.Height, scale)
}
