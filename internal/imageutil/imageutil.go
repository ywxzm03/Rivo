// Package imageutil 负责图片魔数识别与内联前处理。
// read 工具与剪贴板附件共用同一套尺寸与体积限制，
// 因此独立成包，避免 UI 侧反向依赖工具实现。
package imageutil

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"

	"github.com/disintegration/imaging"
	"golang.org/x/image/webp"
)

const (
	maxWidth  = 2000
	maxHeight = 2000
	maxBytes  = 4*1024*1024 + 512*1024
)

var jpegQualities = []int{85, 80, 70, 55, 40}

var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

var (
	errDecode = errors.New("[Image omitted: could not be converted to a supported inline image format.]")
	errTooBig = errors.New("[Image omitted: could not be resized below the inline image size limit.]")
)

// Data 是经过统一尺寸和体积限制处理后的内联图片
type Data struct {
	Data           string
	MIMEType       string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
}

// Sniff 只看开头的魔数，返回 image/jpeg、image/png、image/webp 或空串
func Sniff(data []byte) string {
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

// Process 把图片压到内联限制内，返回 base64 与最终 MIME
func Process(data []byte, mimeType string) (Data, error) {
	result, err := process(data, mimeType, limits{maxWidth: maxWidth, maxHeight: maxHeight, maxBytes: maxBytes})
	if err != nil {
		return Data{}, err
	}
	return Data{
		Data:           result.data,
		MIMEType:       result.mimeType,
		OriginalWidth:  result.originalWidth,
		OriginalHeight: result.originalHeight,
		Width:          result.width,
		Height:         result.height,
	}, nil
}

type limits struct {
	maxWidth  int
	maxHeight int
	maxBytes  int
}

type result struct {
	data           string
	mimeType       string
	originalWidth  int
	originalHeight int
	width          int
	height         int
}

func decode(data []byte, mimeType string) (image.Image, error) {
	reader := bytes.NewReader(data)
	switch mimeType {
	case "image/jpeg":
		return imaging.Decode(reader, imaging.AutoOrientation(true))
	case "image/png":
		return png.Decode(reader)
	case "image/webp":
		return webp.Decode(reader)
	}
	return nil, errDecode
}

func process(data []byte, mimeType string, limit limits) (result, error) {
	img, err := decode(data, mimeType)
	if err != nil {
		return result{}, errDecode
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	out := result{
		mimeType:       mimeType,
		originalWidth:  width,
		originalHeight: height,
		width:          width,
		height:         height,
	}

	// 1.已在限内就原样返回，不重新编码
	original := base64.StdEncoding.EncodeToString(data)
	if width <= limit.maxWidth && height <= limit.maxHeight && len(original) < limit.maxBytes {
		out.data = original
		return out, nil
	}

	// 2.等比缩到尺寸上限内
	targetWidth, targetHeight := width, height
	if targetWidth > limit.maxWidth {
		targetHeight = targetHeight * limit.maxWidth / targetWidth
		targetWidth = limit.maxWidth
	}
	if targetHeight > limit.maxHeight {
		targetWidth = targetWidth * limit.maxHeight / targetHeight
		targetHeight = limit.maxHeight
	}

	// 3.逐步缩小，每轮先试 PNG 再试各档质量的 JPEG
	for {
		resized := imaging.Resize(img, targetWidth, targetHeight, imaging.Lanczos)
		for _, candidate := range encodeCandidates(resized) {
			if len(candidate.data) < limit.maxBytes {
				out.data = candidate.data
				out.mimeType = candidate.mimeType
				out.width = targetWidth
				out.height = targetHeight
				return out, nil
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
	return result{}, errTooBig
}

type encoded struct {
	data     string
	mimeType string
}

// encodeCandidates 依次产出 PNG 与各档质量的 JPEG，顺序决定优先选谁
func encodeCandidates(img image.Image) []encoded {
	out := make([]encoded, 0, 1+len(jpegQualities))
	if data, err := encode(img, imaging.PNG, 0); err == nil {
		out = append(out, encoded{data: data, mimeType: "image/png"})
	}
	for _, quality := range jpegQualities {
		if data, err := encode(img, imaging.JPEG, quality); err == nil {
			out = append(out, encoded{data: data, mimeType: "image/jpeg"})
		}
	}
	return out
}

func encode(img image.Image, format imaging.Format, quality int) (string, error) {
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
