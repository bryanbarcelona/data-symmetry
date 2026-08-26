package dupekill

// Blank imports register every supported decoder with image.Decode /
// image.DecodeConfig so pixel-hash mode works across all common formats
// without any format-specific logic elsewhere in this package.
import (
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "github.com/gen2brain/heic"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)
