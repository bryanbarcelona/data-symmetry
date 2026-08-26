package dupekill

import (
	"crypto/sha256"
	"fmt"
	"image"
	"image/draw"
	"io"
	"os"
	"sync"

	"github.com/schollz/progressbar/v3"
)

// dimensions is the pre-filter key for pixel-hash mode: two images can only
// have identical pixels if they have identical width and height.
type dimensions struct {
	width  int
	height int
}

// probeDimensions reads just the header of each file to determine image
// dimensions, without decoding pixel data. Files that fail to decode as an
// image (wrong format, corrupt, not an image at all) are left with isImage
// false and excluded from pixel-hash comparison.
func probeDimensions(files []*file, out io.Writer) {
	if len(files) == 0 {
		return
	}

	bar := progressbar.NewOptions64(int64(len(files)),
		progressbar.OptionSetDescription("Reading image dimensions"),
		progressbar.OptionSetWriter(out),
		progressbar.OptionShowCount(),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerHead:    "█",
			SaucerPadding: "░",
			BarStart:      "|",
			BarEnd:        "|",
		}),
	)
	defer bar.Finish()

	jobs := make(chan *file, len(files))
	var wg sync.WaitGroup
	numWorkers := 32
	if len(files) < numWorkers {
		numWorkers = len(files)
	}

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if cfg, err := decodeImageConfig(f.abs); err == nil {
					f.isImage = true
					f.width = cfg.Width
					f.height = cfg.Height
				}
				bar.Add(1)
			}
		}()
	}

	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()
}

func decodeImageConfig(path string) (image.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Config{}, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	return cfg, err
}

// computePixelHash decodes an image and hashes its canonical pixel buffer
// rather than its encoded bytes. Decoding into image.NRGBA normalizes away
// differences in source format, color model, and encoder internals (e.g. a
// JPEG's YCbCr planes vs. a PNG's paletted pixels), so two files with
// identical pixels hash identically regardless of container format or
// embedded metadata.
func computePixelHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return "", err
	}

	bounds := img.Bounds()
	canonical := image.NewNRGBA(bounds)
	draw.Draw(canonical, bounds, img, bounds.Min, draw.Src)

	h := sha256.New()
	fmt.Fprintf(h, "%dx%d", bounds.Dx(), bounds.Dy())
	h.Write(canonical.Pix)
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// filterByDimensions applies the pixel-hash cross-tree pre-filter: two files
// can only be pixel-identical if their dimensions match, so both trees are
// probed and reduced to that overlap before the expensive full decode+hash.
func filterByDimensions(referenceFiles, cleanupFiles []*file, out io.Writer) (filteredRef, filteredCleanup []*file) {
	fmt.Fprintln(out, "Reading image dimensions...")
	probeDimensions(referenceFiles, os.Stdout)
	probeDimensions(cleanupFiles, os.Stdout)

	cleanupDims := make(map[dimensions]bool, len(cleanupFiles))
	skippedCleanup := 0
	for _, f := range cleanupFiles {
		if !f.isImage {
			skippedCleanup++
			continue
		}
		cleanupDims[dimensions{f.width, f.height}] = true
	}

	skippedRef := 0
	for _, f := range referenceFiles {
		if !f.isImage {
			skippedRef++
			continue
		}
		if cleanupDims[dimensions{f.width, f.height}] {
			filteredRef = append(filteredRef, f)
		}
	}

	refDims := make(map[dimensions]bool, len(filteredRef))
	for _, f := range filteredRef {
		refDims[dimensions{f.width, f.height}] = true
	}
	for _, f := range cleanupFiles {
		if f.isImage && refDims[dimensions{f.width, f.height}] {
			filteredCleanup = append(filteredCleanup, f)
		}
	}

	fmt.Fprintf(out, "Skipped %d non-image reference files, %d non-image cleanup files\n", skippedRef, skippedCleanup)
	fmt.Fprintf(out, "Dimension pre-filter: hashing %d/%d reference, %d/%d cleanup images\n",
		len(filteredRef), len(referenceFiles), len(filteredCleanup), len(cleanupFiles))

	return filteredRef, filteredCleanup
}

// filterByDimensionCount applies the pixel-hash single-tree pre-filter: only
// images whose dimensions recur at least twice can possibly be duplicates.
func filterByDimensionCount(files []*file, out io.Writer) []*file {
	fmt.Fprintln(out, "Reading image dimensions...")
	probeDimensions(files, os.Stdout)

	dimCount := make(map[dimensions]int, len(files))
	skipped := 0
	for _, f := range files {
		if !f.isImage {
			skipped++
			continue
		}
		dimCount[dimensions{f.width, f.height}]++
	}

	var filtered []*file
	for _, f := range files {
		if f.isImage && dimCount[dimensions{f.width, f.height}] >= 2 {
			filtered = append(filtered, f)
		}
	}

	fmt.Fprintf(out, "Skipped %d non-image files\n", skipped)
	fmt.Fprintf(out, "Dimension pre-filter: hashing %d/%d images\n", len(filtered), len(files))

	return filtered
}

// hashImages computes pixel hashes for files concurrently, mirroring the
// worker-pool shape of hashFiles.
func hashImages(files []*file, out io.Writer) {
	type job struct {
		index int
		file  *file
	}

	if len(files) == 0 {
		return
	}

	bar := progressbar.NewOptions64(int64(len(files)),
		progressbar.OptionSetDescription("Computing pixel hashes"),
		progressbar.OptionSetWriter(out),
		progressbar.OptionShowCount(),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerHead:    "█",
			SaucerPadding: "░",
			BarStart:      "|",
			BarEnd:        "|",
		}),
	)
	defer bar.Finish()

	jobs := make(chan job, len(files))
	results := make(chan struct {
		index int
		hash  string
	}, len(files))

	var wg sync.WaitGroup
	numWorkers := 32
	if len(files) < numWorkers {
		numWorkers = len(files)
	}

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if hash, err := computePixelHash(job.file.abs); err == nil {
					results <- struct {
						index int
						hash  string
					}{job.index, hash}
				}
				bar.Add(1)
			}
		}()
	}

	go func() {
		for i, f := range files {
			jobs <- job{index: i, file: f}
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		files[result.index].pixelHash = result.hash
	}
}
