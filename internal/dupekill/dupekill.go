package dupekill

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

type Mode string

const (
	ModePathOnly     Mode = "path"
	ModePathSize     Mode = "path+size"
	ModePathHash     Mode = "path+hash"
	ModeHashOnly     Mode = "hash"
	ModeFilenameSize Mode = "filename+size"
	ModePixelHash    Mode = "pixel-hash"
)

const (
	partialSegmentSize = 1 * 1024 * 1024       // 1 MB per segment
	partialThreshold   = 3 * partialSegmentSize // files below this are fully hashed
)

type file struct {
	root      string
	rel       string
	abs       string
	size      int64
	hash      string
	isImage   bool
	width     int
	height    int
	pixelHash string
}

type duplicate struct {
	reference *file
	cleanup   []*file
}

func normalizeForComparison(s string, ignoreCase bool) string {
	if ignoreCase {
		return strings.ToLower(s)
	}
	return s
}

func scanTree(root string, out io.Writer) ([]*file, error) {
	var files []*file
	var mu sync.Mutex
	var wg sync.WaitGroup

	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}

	bar := progressbar.NewOptions64(-1,
		progressbar.OptionSetDescription("Scanning "+root),
		progressbar.OptionSetWriter(out),
		progressbar.OptionShowCount(),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerHead:    "█",
			SaucerPadding: "░",
			BarStart:      "|",
			BarEnd:        "|",
		}),
	)
	defer bar.Finish()

	var scanDir func(string)
	scanDir = func(current string) {
		defer wg.Done()
		entries, err := os.ReadDir(current)
		if err != nil {
			return
		}
		for _, entry := range entries {
			fullPath := filepath.Join(current, entry.Name())
			if entry.IsDir() {
				wg.Add(1)
				go scanDir(fullPath)
			} else {
				info, err := entry.Info()
				if err != nil {
					continue
				}
				rel, err := filepath.Rel(root, fullPath)
				if err != nil {
					continue
				}
				mu.Lock()
				files = append(files, &file{
					root: root,
					rel:  rel,
					abs:  fullPath,
					size: info.Size(),
				})
				mu.Unlock()
				bar.Add(1)
			}
		}
	}

	wg.Add(1)
	scanDir(root)
	wg.Wait()
	return files, nil
}

func hashFiles(files []*file, partial bool, out io.Writer) {
	type job struct {
		index int
		file  *file
	}

	if len(files) == 0 {
		return
	}

	bar := progressbar.NewOptions64(int64(len(files)),
		progressbar.OptionSetDescription("Computing file hashes"),
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
				var hash string
				var err error
				if partial {
					hash, err = computePartialHash(job.file.abs, job.file.size)
				} else {
					hash, err = computeHash(job.file.abs)
				}
				if err == nil {
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
		files[result.index].hash = result.hash
	}
}

func computeHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// computePartialHash hashes three 1 MB segments (header, center, footer) for
// files at or above partialThreshold, falling back to a full hash for smaller files.
func computePartialHash(path string, size int64) (string, error) {
	if size < partialThreshold {
		return computeHash(path)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, partialSegmentSize)

	// Header: first 1 MB
	if _, err := io.ReadFull(f, buf); err != nil {
		return computeHash(path)
	}
	h.Write(buf)

	// Center: 1 MB from exact midpoint
	mid := size/2 - partialSegmentSize/2
	if _, err := f.Seek(mid, io.SeekStart); err != nil {
		return computeHash(path)
	}
	if _, err := io.ReadFull(f, buf); err != nil {
		return computeHash(path)
	}
	h.Write(buf)

	// Footer: last 1 MB
	if _, err := f.Seek(-int64(partialSegmentSize), io.SeekEnd); err != nil {
		return computeHash(path)
	}
	if _, err := io.ReadFull(f, buf); err != nil {
		return computeHash(path)
	}
	h.Write(buf)

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func findDuplicates(referenceFiles, cleanupFiles []*file, mode Mode, ignoreCase bool, partial, unsafePartial bool, out *os.File) []duplicate {
	fmt.Fprintf(out, "Finding duplicates using %s mode (case-insensitive: %v)...\n", mode, ignoreCase)
	if partial {
		if unsafePartial {
			fmt.Fprintln(out, "Partial hash mode: 3×1 MB segments — verification SKIPPED (--unsafe)")
		} else {
			fmt.Fprintln(out, "Partial hash mode: 3×1 MB segments — full-hash verify enabled")
		}
	}

	if mode == ModePathHash || mode == ModeHashOnly {
		fmt.Fprintln(out, "Computing file hashes...")

		if mode == ModeHashOnly {
			// Size pre-filter: only hash files whose size appears in both trees.
			cleanupSizes := make(map[int64]bool, len(cleanupFiles))
			for _, f := range cleanupFiles {
				cleanupSizes[f.size] = true
			}
			var filteredRef []*file
			for _, f := range referenceFiles {
				if cleanupSizes[f.size] {
					filteredRef = append(filteredRef, f)
				}
			}
			refSizes := make(map[int64]bool, len(filteredRef))
			for _, f := range filteredRef {
				refSizes[f.size] = true
			}
			var filteredCleanup []*file
			for _, f := range cleanupFiles {
				if refSizes[f.size] {
					filteredCleanup = append(filteredCleanup, f)
				}
			}
			fmt.Fprintf(out, "Size pre-filter: hashing %d/%d reference, %d/%d cleanup files\n",
				len(filteredRef), len(referenceFiles), len(filteredCleanup), len(cleanupFiles))
			hashFiles(filteredRef, partial, os.Stdout)
			hashFiles(filteredCleanup, partial, os.Stdout)
		} else {
			hashFiles(referenceFiles, partial, os.Stdout)
			hashFiles(cleanupFiles, partial, os.Stdout)
		}
	} else if mode == ModePixelHash {
		filteredRef, filteredCleanup := filterByDimensions(referenceFiles, cleanupFiles, out)
		fmt.Fprintln(out, "Computing pixel hashes...")
		hashImages(filteredRef, os.Stdout)
		hashImages(filteredCleanup, os.Stdout)
	}

	referenceIndex := make(map[string]*file)
	switch mode {
	case ModePathOnly:
		for _, f := range referenceFiles {
			key := normalizeForComparison(f.rel, ignoreCase)
			referenceIndex[key] = f
		}
	case ModePathSize:
		for _, f := range referenceFiles {
			key := normalizeForComparison(f.rel, ignoreCase) + "|" + fmt.Sprintf("%d", f.size)
			referenceIndex[key] = f
		}
	case ModeFilenameSize:
		for _, f := range referenceFiles {
			key := normalizeForComparison(filepath.Base(f.rel), ignoreCase) + "|" + fmt.Sprintf("%d", f.size)
			referenceIndex[key] = f
		}
	case ModePathHash:
		for _, f := range referenceFiles {
			if f.hash != "" {
				key := normalizeForComparison(f.rel, ignoreCase) + "|" + f.hash
				referenceIndex[key] = f
			}
		}
	case ModeHashOnly:
		for _, f := range referenceFiles {
			if f.hash != "" {
				referenceIndex[f.hash] = f
			}
		}
	case ModePixelHash:
		for _, f := range referenceFiles {
			if f.pixelHash != "" {
				referenceIndex[f.pixelHash] = f
			}
		}
	}

	duplicates := make(map[string]*duplicate)

	for _, cleanupFile := range cleanupFiles {
		var key string
		switch mode {
		case ModePathOnly:
			key = normalizeForComparison(cleanupFile.rel, ignoreCase)
		case ModePathSize:
			key = normalizeForComparison(cleanupFile.rel, ignoreCase) + "|" + fmt.Sprintf("%d", cleanupFile.size)
		case ModeFilenameSize:
			key = normalizeForComparison(filepath.Base(cleanupFile.rel), ignoreCase) + "|" + fmt.Sprintf("%d", cleanupFile.size)
		case ModePathHash:
			if cleanupFile.hash != "" {
				key = normalizeForComparison(cleanupFile.rel, ignoreCase) + "|" + cleanupFile.hash
			}
		case ModeHashOnly:
			if cleanupFile.hash != "" {
				key = cleanupFile.hash
			}
		case ModePixelHash:
			if cleanupFile.pixelHash != "" {
				key = cleanupFile.pixelHash
			}
		}

		if key != "" {
			if refFile, exists := referenceIndex[key]; exists {
				if dup, exists := duplicates[key]; exists {
					dup.cleanup = append(dup.cleanup, cleanupFile)
				} else {
					duplicates[key] = &duplicate{
						reference: refFile,
						cleanup:   []*file{cleanupFile},
					}
				}
			}
		}
	}

	// Safe partial-hash verification: re-confirm each match with a full hash.
	if mode == ModeHashOnly && partial && !unsafePartial {
		fmt.Fprintf(out, "Verifying %d partial hash match groups with full hash...\n", len(duplicates))
		for key, dup := range duplicates {
			refFull, err := computeHash(dup.reference.abs)
			if err != nil {
				delete(duplicates, key)
				continue
			}
			var verified []*file
			for _, cf := range dup.cleanup {
				cfFull, err := computeHash(cf.abs)
				if err != nil {
					continue
				}
				if cfFull == refFull {
					verified = append(verified, cf)
				}
			}
			if len(verified) == 0 {
				delete(duplicates, key)
			} else {
				dup.cleanup = verified
			}
		}
	}

	var result []duplicate
	for _, dup := range duplicates {
		sort.Slice(dup.cleanup, func(i, j int) bool {
			return dup.cleanup[i].abs < dup.cleanup[j].abs
		})
		result = append(result, *dup)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].reference.abs < result[j].reference.abs
	})

	fmt.Fprintf(out, "Found %d duplicate groups\n", len(result))
	return result
}

// findInternalDuplicates finds duplicates within a single tree.
// The alphabetically first absolute path in each group is kept.
func findInternalDuplicates(files []*file, mode Mode, ignoreCase bool, partial, unsafePartial bool, out *os.File) []duplicate {
	fmt.Fprintf(out, "Finding internal duplicates using %s mode (case-insensitive: %v)...\n", mode, ignoreCase)
	if partial {
		if unsafePartial {
			fmt.Fprintln(out, "Partial hash mode: 3×1 MB segments — verification SKIPPED (--unsafe)")
		} else {
			fmt.Fprintln(out, "Partial hash mode: 3×1 MB segments — full-hash verify enabled")
		}
	}

	if mode == ModePathHash || mode == ModeHashOnly {
		fmt.Fprintln(out, "Computing file hashes...")
		if mode == ModeHashOnly {
			// Size pre-filter: only hash files whose size appears at least twice.
			sizeCount := make(map[int64]int, len(files))
			for _, f := range files {
				sizeCount[f.size]++
			}
			var toHash []*file
			for _, f := range files {
				if sizeCount[f.size] >= 2 {
					toHash = append(toHash, f)
				}
			}
			fmt.Fprintf(out, "Size pre-filter: hashing %d/%d files\n", len(toHash), len(files))
			hashFiles(toHash, partial, os.Stdout)
		} else {
			hashFiles(files, partial, os.Stdout)
		}
	} else if mode == ModePixelHash {
		toHash := filterByDimensionCount(files, out)
		fmt.Fprintln(out, "Computing pixel hashes...")
		hashImages(toHash, os.Stdout)
	}

	groups := make(map[string][]*file)

	switch mode {
	case ModePathOnly:
		for _, f := range files {
			key := normalizeForComparison(f.rel, ignoreCase)
			groups[key] = append(groups[key], f)
		}
	case ModePathSize:
		for _, f := range files {
			key := normalizeForComparison(f.rel, ignoreCase) + "|" + fmt.Sprintf("%d", f.size)
			groups[key] = append(groups[key], f)
		}
	case ModeFilenameSize:
		for _, f := range files {
			key := normalizeForComparison(filepath.Base(f.rel), ignoreCase) + "|" + fmt.Sprintf("%d", f.size)
			groups[key] = append(groups[key], f)
		}
	case ModePathHash:
		for _, f := range files {
			if f.hash != "" {
				key := normalizeForComparison(f.rel, ignoreCase) + "|" + f.hash
				groups[key] = append(groups[key], f)
			}
		}
	case ModeHashOnly:
		for _, f := range files {
			if f.hash != "" {
				groups[f.hash] = append(groups[f.hash], f)
			}
		}
	case ModePixelHash:
		for _, f := range files {
			if f.pixelHash != "" {
				groups[f.pixelHash] = append(groups[f.pixelHash], f)
			}
		}
	}

	// Safe partial-hash verification: re-hash each group fully and re-split.
	// A partial-hash group can legitimately split into multiple full-hash subgroups.
	if mode == ModeHashOnly && partial && !unsafePartial {
		fmt.Fprintln(out, "Verifying partial hash match groups with full hash...")
		verified := make(map[string][]*file)
		for _, group := range groups {
			if len(group) < 2 {
				continue
			}
			subGroups := make(map[string][]*file)
			for _, f := range group {
				fh, err := computeHash(f.abs)
				if err != nil {
					continue
				}
				subGroups[fh] = append(subGroups[fh], f)
			}
			for fh, sg := range subGroups {
				if len(sg) >= 2 {
					verified[fh] = sg
				}
			}
		}
		groups = verified
	}

	var result []duplicate
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool {
			return group[i].abs < group[j].abs
		})
		result = append(result, duplicate{
			reference: group[0],
			cleanup:   group[1:],
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].reference.abs < result[j].reference.abs
	})

	for i := range result {
		sort.Slice(result[i].cleanup, func(a, b int) bool {
			return result[i].cleanup[a].abs < result[i].cleanup[b].abs
		})
	}

	fmt.Fprintf(out, "Found %d duplicate groups\n", len(result))
	return result
}

func output(outFile *os.File, s string) {
	if outFile != nil {
		fmt.Fprintln(outFile, s)
	} else {
		fmt.Println(s)
	}
}

func processDuplicates(duplicates []duplicate, dryRun bool, delete bool, moveTo string, outFile *os.File) error {
	totalDupes := 0
	for _, dup := range duplicates {
		totalDupes += len(dup.cleanup)
	}

	if dryRun || !delete {
		for i, dup := range duplicates {
			output(outFile, fmt.Sprintf("\nGroup %d:", i+1))
			output(outFile, fmt.Sprintf("  Reference: %s", dup.reference.abs))
			for _, f := range dup.cleanup {
				action := "Delete"
				if moveTo != "" {
					action = "Move"
				}
				output(outFile, fmt.Sprintf("  %s: %s", action, f.abs))
			}
		}
		output(outFile, fmt.Sprintf("\nWould remove %d duplicate files across %d groups", totalDupes, len(duplicates)))
		output(outFile, "\nDry-run enabled. No files affected.")
		return nil
	}

	fmt.Printf("\nThis will %s %d files. Confirm (y/N): ",
		map[bool]string{true: "delete", false: "move"}[moveTo == ""], totalDupes)

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
		output(outFile, "Aborted.")
		return nil
	}

	var failed int
	for _, dup := range duplicates {
		for _, f := range dup.cleanup {
			var err error
			if moveTo != "" {
				dest := filepath.Join(moveTo, filepath.Base(f.abs))
				err = os.Rename(f.abs, dest)
			} else {
				err = os.Remove(f.abs)
			}

			if err != nil {
				output(outFile, fmt.Sprintf("Failed to process %s: %v", f.abs, err))
				failed++
			}
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d operations failed", failed)
	}

	output(outFile, fmt.Sprintf("Successfully processed %d duplicate files", totalDupes))
	return nil
}

func removeEmptyDirs(roots []string, dryRun bool, outFile *os.File) {
	for _, root := range roots {
		output(outFile, fmt.Sprintf("Cleaning empty directories in: %s", root))
		removed := removeEmptyDirsRecursive(root, dryRun, outFile)
		output(outFile, fmt.Sprintf("Removed %d empty directories", removed))
	}
}

func removeEmptyDirsRecursive(dir string, dryRun bool, outFile *os.File) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	removedCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			fullPath := filepath.Join(dir, entry.Name())
			removedCount += removeEmptyDirsRecursive(fullPath, dryRun, outFile)
		}
	}

	entries, err = os.ReadDir(dir)
	if err != nil {
		return removedCount
	}

	if len(entries) == 0 && dir != "" {
		if dryRun {
			output(outFile, fmt.Sprintf("  Would remove empty directory: %s", dir))
		} else {
			if err := os.Remove(dir); err == nil {
				output(outFile, fmt.Sprintf("  Removed empty directory: %s", dir))
				removedCount++
			}
		}
	}

	return removedCount
}

func run(cmd *cobra.Command, args []string) error {
	reference, _ := cmd.Flags().GetString("reference")
	cleanup, _ := cmd.Flags().GetStringSlice("cleanup")
	modeStr, _ := cmd.Flags().GetString("mode")
	moveTo, _ := cmd.Flags().GetString("move-to")
	outPath, _ := cmd.Flags().GetString("out")
	keepEmptyDirs, _ := cmd.Flags().GetBool("keep-empty-dirs")
	ignoreCase, _ := cmd.Flags().GetBool("ignore-case")
	partialHash, _ := cmd.Flags().GetBool("partial-hash")
	unsafePartial, _ := cmd.Flags().GetBool("unsafe")

	mode := Mode(modeStr)
	if mode != ModePathOnly && mode != ModePathSize && mode != ModeFilenameSize && mode != ModePathHash && mode != ModeHashOnly && mode != ModePixelHash {
		return fmt.Errorf("invalid mode: %s (use: path, path+size, filename+size, path+hash, hash, pixel-hash)", modeStr)
	}

	if unsafePartial && !partialHash {
		return fmt.Errorf("--unsafe requires --partial-hash")
	}
	if partialHash && mode != ModeHashOnly && mode != ModePathHash {
		return fmt.Errorf("--partial-hash only applies to hash and path+hash modes")
	}

	// --- flexible validation for solo cleanup vs ref+cleanup ---
	if reference != "" && len(cleanup) == 0 {
		return fmt.Errorf("--cleanup is required when --reference is provided")
	}
	if reference == "" && len(cleanup) == 0 {
		return fmt.Errorf("either --reference + --cleanup, or --cleanup alone is required")
	}
	if reference == "" && len(cleanup) > 1 {
		return fmt.Errorf("internal dedupe requires exactly one --cleanup directory")
	}

	var outFile *os.File
	if outPath != "" {
		var err error
		outFile, err = os.Create(outPath)
		if err != nil {
			return err
		}
		defer outFile.Close()
	}

	if mode == ModePathOnly && outFile != nil {
		fmt.Fprintln(outFile, "\n⚠️  WARNING: Using 'path' mode - files matched by path ONLY!")
		fmt.Fprintln(outFile, "   Files with different content but same path will be considered duplicates.")
		fmt.Fprintln(outFile, "   This is UNSAFE unless you have identical directory structures.")
	}

	start := time.Now()

	var duplicates []duplicate
	var emptyDirRoots []string

	if reference == "" {
		// --- SOLO CLEANUP = INTERNAL DEDUPE ---
		soloTree := cleanup[0]
		output(outFile, fmt.Sprintf("Scanning tree: %s", soloTree))
		files, err := scanTree(soloTree, os.Stdout)
		if err != nil {
			return err
		}
		output(outFile, fmt.Sprintf("Found %d files", len(files)))

		duplicates = findInternalDuplicates(files, mode, ignoreCase, partialHash, unsafePartial, outFile)
		emptyDirRoots = []string{soloTree}
	} else {
		// --- REFERENCE + CLEANUP ---
		output(outFile, fmt.Sprintf("Scanning reference tree: %s", reference))
		referenceFiles, err := scanTree(reference, os.Stdout)
		if err != nil {
			return err
		}
		output(outFile, fmt.Sprintf("Found %d files in reference tree", len(referenceFiles)))

		var allCleanupFiles []*file
		for _, cleanupTree := range cleanup {
			output(outFile, fmt.Sprintf("Scanning cleanup tree: %s", cleanupTree))
			cleanupFiles, err := scanTree(cleanupTree, os.Stdout)
			if err != nil {
				return err
			}
			output(outFile, fmt.Sprintf("Found %d files in cleanup tree", len(cleanupFiles)))
			allCleanupFiles = append(allCleanupFiles, cleanupFiles...)
		}

		duplicates = findDuplicates(referenceFiles, allCleanupFiles, mode, ignoreCase, partialHash, unsafePartial, outFile)
		emptyDirRoots = cleanup
	}

	if len(duplicates) == 0 {
		output(outFile, "No duplicates found.")
		return nil
	}

	// Always show dry-run first
	output(outFile, "\n=== DRY RUN RESULTS ===")
	if err := processDuplicates(duplicates, true, false, moveTo, outFile); err != nil {
		return err
	}

	// Ask for confirmation
	fmt.Println("\nProceed with operations? (y/N): ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		fmt.Println("Aborted.")
		return nil
	}

	response := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if response != "y" && response != "yes" {
		fmt.Println("Aborted.")
		return nil
	}

	// Perform actual operations
	output(outFile, "\n=== DELETION OPERATIONS ===")
	if err := processDuplicates(duplicates, false, true, moveTo, outFile); err != nil {
		return err
	}

	// Empty directory cleanup (if not disabled)
	if !keepEmptyDirs {
		output(outFile, "\n=== Empty Directory Cleanup ===")
		removeEmptyDirs(emptyDirRoots, false, outFile)
	}

	elapsed := time.Since(start)
	output(outFile, fmt.Sprintf("\nDone in %v.", elapsed))
	return nil
}

var Cmd = &cobra.Command{
	Use:   "dupekill",
	Short: "Remove duplicate files from cleanup trees that exist in reference tree",
	RunE:  run,
}

func init() {
	Cmd.Flags().String("reference", "", "reference tree (files to keep, never modified)")
	Cmd.Flags().StringSlice("cleanup", nil, "trees to clean up (remove duplicates from); use alone for internal dedupe")
	Cmd.Flags().String("mode", "hash", "dedup mode: path | path+size | filename+size | path+hash | hash | pixel-hash")
	Cmd.Flags().String("move-to", "", "move duplicates to directory")
	Cmd.Flags().String("out", "", "output report file")
	Cmd.Flags().Bool("keep-empty-dirs", false, "keep empty directories (default: remove them after deduplication)")
	Cmd.Flags().Bool("ignore-case", true, "case-insensitive path/filename matching (default: true)")
	Cmd.Flags().Bool("partial-hash", false, "hash 3×1 MB segments (header+center+footer) instead of full files; fastest for large media; requires hash or path+hash mode")
	Cmd.Flags().Bool("unsafe", false, "with --partial-hash: skip full-hash verification of matches (faster, near-zero false positive risk for photos/videos)")
}
