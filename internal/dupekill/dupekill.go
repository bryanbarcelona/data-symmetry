// mytool/dupekill/dupekill.go
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

	"github.com/spf13/cobra"
)

type Mode string

const (
	ModePathName Mode = "path+name"
	ModePathHash Mode = "path+hash"
	ModeHashOnly Mode = "hash"
)

type file struct {
	root string
	rel  string
	abs  string
	size int64
	hash string
}

type duplicate struct {
	reference *file   // file in reference tree
	cleanup   []*file // duplicates in cleanup trees
}

func scanTree(root string) ([]*file, error) {
	var files []*file
	var mu sync.Mutex
	var wg sync.WaitGroup

	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}

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
			}
		}
	}

	wg.Add(1)
	scanDir(root)
	wg.Wait()
	return files, nil
}

func hashFiles(files []*file) {
	type job struct {
		index int
		file  *file
	}

	if len(files) == 0 {
		return
	}

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
				hash, err := computeHash(job.file.abs)
				if err == nil {
					results <- struct {
						index int
						hash  string
					}{job.index, hash}
				}
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

func findDuplicates(referenceFiles, cleanupFiles []*file, mode Mode, out *os.File) []duplicate {
	fmt.Fprintf(out, "Finding duplicates using %s mode...\n", mode)

	// Hash files if needed for hash-based modes
	if mode != ModePathName {
		fmt.Fprintln(out, "Computing file hashes...")
		hashFiles(referenceFiles)
		hashFiles(cleanupFiles)
	}

	// Build reference index
	referenceIndex := make(map[string]*file)
	switch mode {
	case ModePathName:
		for _, f := range referenceFiles {
			// For path+name, include size in the key to ensure exact match
			key := f.rel + "|" + fmt.Sprintf("%d", f.size)
			referenceIndex[key] = f
		}
	case ModePathHash:
		for _, f := range referenceFiles {
			if f.hash != "" {
				referenceIndex[f.rel+"|"+f.hash] = f
			}
		}
	case ModeHashOnly:
		for _, f := range referenceFiles {
			if f.hash != "" {
				referenceIndex[f.hash] = f
			}
		}
	}

	// Find duplicates in cleanup trees
	duplicates := make(map[string]*duplicate)

	for _, cleanupFile := range cleanupFiles {
		var key string
		switch mode {
		case ModePathName:
			// Include size in the key for exact matching
			key = cleanupFile.rel + "|" + fmt.Sprintf("%d", cleanupFile.size)
		case ModePathHash:
			if cleanupFile.hash != "" {
				key = cleanupFile.rel + "|" + cleanupFile.hash
			}
		case ModeHashOnly:
			if cleanupFile.hash != "" {
				key = cleanupFile.hash
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

	// Convert to slice
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

	output(outFile, fmt.Sprintf("\nWould remove %d duplicate files across %d groups", totalDupes, len(duplicates)))

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

// removeEmptyDirs recursively removes empty directories
func removeEmptyDirs(roots []string, dryRun bool, outFile *os.File) {
	for _, root := range roots {
		output(outFile, fmt.Sprintf("Cleaning empty directories in: %s", root))
		removed := removeEmptyDirsRecursive(root, dryRun, outFile)
		output(outFile, fmt.Sprintf("Removed %d empty directories", removed))
	}
}

// removeEmptyDirsRecursive does the actual work and returns count of removed dirs
func removeEmptyDirsRecursive(dir string, dryRun bool, outFile *os.File) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	// First, recursively process subdirectories
	removedCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			fullPath := filepath.Join(dir, entry.Name())
			removedCount += removeEmptyDirsRecursive(fullPath, dryRun, outFile)
		}
	}

	// Re-read directory to see if it's now empty (after processing subdirs)
	entries, err = os.ReadDir(dir)
	if err != nil {
		return removedCount
	}

	// If directory is empty (and not the root of our cleanup trees), remove it
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

	mode := Mode(modeStr)
	if mode != ModePathName && mode != ModePathHash && mode != ModeHashOnly {
		return fmt.Errorf("invalid mode: %s (use: path+name, path+hash, hash)", modeStr)
	}

	if len(cleanup) == 0 {
		return fmt.Errorf("at least one cleanup directory required")
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

	start := time.Now()

	// Scan reference tree
	output(outFile, fmt.Sprintf("Scanning reference tree: %s", reference))
	referenceFiles, err := scanTree(reference)
	if err != nil {
		return err
	}
	output(outFile, fmt.Sprintf("Found %d files in reference tree", len(referenceFiles)))

	// Scan cleanup trees
	var allCleanupFiles []*file
	for _, cleanupTree := range cleanup {
		output(outFile, fmt.Sprintf("Scanning cleanup tree: %s", cleanupTree))
		cleanupFiles, err := scanTree(cleanupTree)
		if err != nil {
			return err
		}
		output(outFile, fmt.Sprintf("Found %d files in cleanup tree", len(cleanupFiles)))
		allCleanupFiles = append(allCleanupFiles, cleanupFiles...)
	}

	duplicates := findDuplicates(referenceFiles, allCleanupFiles, mode, outFile)
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
		removeEmptyDirs(cleanup, false, outFile)
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
	Cmd.Flags().StringSlice("cleanup", nil, "trees to clean up (remove duplicates from)")
	Cmd.Flags().String("mode", "hash", "dedup mode: path+name | path+hash | hash")
	Cmd.Flags().String("move-to", "", "move duplicates to directory")
	Cmd.Flags().String("out", "", "output report file")
	Cmd.Flags().Bool("keep-empty-dirs", false, "keep empty directories (default: remove them after deduplication)")
	Cmd.MarkFlagRequired("reference")
	Cmd.MarkFlagRequired("cleanup")
}
