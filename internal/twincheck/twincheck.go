package twincheck

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

type FileMap map[string]int64

func getFilesConcurrent(base string) (FileMap, error) {
	files := make(FileMap)
	var mu sync.Mutex
	var wg sync.WaitGroup

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
				rel, err := filepath.Rel(base, fullPath)
				if err != nil {
					continue
				}
				mu.Lock()
				files[rel] = info.Size()
				mu.Unlock()
			}
		}
	}

	wg.Add(1)
	scanDir(base)
	wg.Wait()
	return files, nil
}

func hashFile(path string) (string, error) {
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

func hashFiles(base string, paths []string) map[string]string {
	if len(paths) == 0 {
		return make(map[string]string)
	}

	numWorkers := 32 // adjust based on your system; 32 is safe for I/O
	if len(paths) < numWorkers {
		numWorkers = len(paths)
	}

	jobs := make(chan string, len(paths))
	results := make(chan struct {
		path string
		hash string
	}, len(paths))

	var wg sync.WaitGroup

	// Launch workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range jobs {
				if h, err := hashFile(filepath.Join(base, rel)); err == nil {
					results <- struct {
						path string
						hash string
					}{rel, h}
				}
			}
		}()
	}

	// Send jobs
	go func() {
		for _, p := range paths {
			jobs <- p
		}
		close(jobs)
	}()

	// Close results when workers are done
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	hashes := make(map[string]string)
	for res := range results {
		hashes[res.path] = res.hash
	}

	return hashes
}

func buildSizeMap(fm FileMap) map[int64][]string {
	sizeMap := make(map[int64][]string)
	for path, size := range fm {
		sizeMap[size] = append(sizeMap[size], path)
	}
	return sizeMap
}

func output(outFile *os.File, s string) {
	if outFile != nil {
		fmt.Fprintln(outFile, s)
	} else {
		fmt.Println(s)
	}
}

// === Mode: off ===
func compareOff(filesA, filesB FileMap, mode string, outFile *os.File) {
	var onlyA, onlyB []string
	for path := range filesA {
		if _, ok := filesB[path]; !ok {
			onlyA = append(onlyA, path)
		}
	}
	for path := range filesB {
		if _, ok := filesA[path]; !ok {
			onlyB = append(onlyB, path)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)

	switch mode {
	case "missing_a":
		output(outFile, fmt.Sprintf("\n=== Files missing in Tree A (%d) ===", len(onlyB)))
		for _, f := range onlyB {
			output(outFile, f)
		}
	case "missing_b":
		output(outFile, fmt.Sprintf("\n=== Files missing in Tree B (%d) ===", len(onlyA)))
		for _, f := range onlyA {
			output(outFile, f)
		}
	case "all":
		if len(onlyA) > 0 {
			output(outFile, fmt.Sprintf("\n=== Only in Tree A (%d) ===", len(onlyA)))
			for _, f := range onlyA {
				output(outFile, f)
			}
		}
		if len(onlyB) > 0 {
			output(outFile, fmt.Sprintf("\n=== Only in Tree B (%d) ===", len(onlyB)))
			for _, f := range onlyB {
				output(outFile, f)
			}
		}
	}
}

// === Mode: smart (your preferred) ===
func compareSmart(driveA, driveB string, mode string, outFile *os.File) error {
	output(outFile, fmt.Sprintf("Scanning %s...", driveA))
	filesA, _ := getFilesConcurrent(driveA)
	output(outFile, fmt.Sprintf("Found %d files in %s", len(filesA), driveA))

	output(outFile, fmt.Sprintf("Scanning %s...", driveB))
	filesB, _ := getFilesConcurrent(driveB)
	output(outFile, fmt.Sprintf("Found %d files in %s", len(filesB), driveB))

	var missingInB, missingInA []string
	for path := range filesA {
		if _, ok := filesB[path]; !ok {
			missingInB = append(missingInB, path)
		}
	}
	for path := range filesB {
		if _, ok := filesA[path]; !ok {
			missingInA = append(missingInA, path)
		}
	}

	sizeMapB := buildSizeMap(filesB)
	sizeMapA := buildSizeMap(filesA)

	var trulyMissingInB, trulyMissingInA []string

	// Process missingInB
	if len(missingInB) > 0 {
		missingBySize := make(map[int64][]string)
		for _, p := range missingInB {
			missingBySize[filesA[p]] = append(missingBySize[filesA[p]], p)
		}

		var toHashA, toHashB []string
		for size, paths := range missingBySize {
			if candidates, exists := sizeMapB[size]; exists && len(candidates) > 0 {
				toHashA = append(toHashA, paths...)
				toHashB = append(toHashB, candidates...)
			} else {
				trulyMissingInB = append(trulyMissingInB, paths...)
			}
		}

		if len(toHashA) > 0 {
			hashesA := hashFiles(driveA, toHashA)
			hashesB := hashFiles(driveB, toHashB)
			hashSetB := make(map[string]bool)
			for _, h := range hashesB {
				hashSetB[h] = true
			}
			for _, p := range toHashA {
				if h, ok := hashesA[p]; ok {
					if !hashSetB[h] {
						trulyMissingInB = append(trulyMissingInB, p)
					}
				} else {
					trulyMissingInB = append(trulyMissingInB, p)
				}
			}
		}
	}

	// Process missingInA
	if len(missingInA) > 0 {
		missingBySize := make(map[int64][]string)
		for _, p := range missingInA {
			missingBySize[filesB[p]] = append(missingBySize[filesB[p]], p)
		}

		var toHashB2, toHashA2 []string
		for size, paths := range missingBySize {
			if candidates, exists := sizeMapA[size]; exists && len(candidates) > 0 {
				toHashB2 = append(toHashB2, paths...)
				toHashA2 = append(toHashA2, candidates...)
			} else {
				trulyMissingInA = append(trulyMissingInA, paths...)
			}
		}

		if len(toHashB2) > 0 {
			hashesB := hashFiles(driveB, toHashB2)
			hashesA := hashFiles(driveA, toHashA2)
			hashSetA := make(map[string]bool)
			for _, h := range hashesA {
				hashSetA[h] = true
			}
			for _, p := range toHashB2 {
				if h, ok := hashesB[p]; ok {
					if !hashSetA[h] {
						trulyMissingInA = append(trulyMissingInA, p)
					}
				} else {
					trulyMissingInA = append(trulyMissingInA, p)
				}
			}
		}
	}

	sort.Strings(trulyMissingInB)
	sort.Strings(trulyMissingInA)

	switch mode {
	case "missing_a":
		output(outFile, fmt.Sprintf("\n=== Files missing in Tree A (%d) ===", len(trulyMissingInA)))
		for _, f := range trulyMissingInA {
			output(outFile, f)
		}
	case "missing_b":
		output(outFile, fmt.Sprintf("\n=== Files missing in Tree B (%d) ===", len(trulyMissingInB)))
		for _, f := range trulyMissingInB {
			output(outFile, f)
		}
	case "all":
		if len(trulyMissingInB) > 0 {
			output(outFile, fmt.Sprintf("\n=== Only in Tree A (%d) ===", len(trulyMissingInB)))
			for _, f := range trulyMissingInB {
				output(outFile, f)
			}
		}
		if len(trulyMissingInA) > 0 {
			output(outFile, fmt.Sprintf("\n=== Only in Tree B (%d) ===", len(trulyMissingInA)))
			for _, f := range trulyMissingInA {
				output(outFile, f)
			}
		}
	}
	return nil
}

// === Mode: strict (global content search) ===
func compareStrict(driveA, driveB string, mode string, outFile *os.File) error {
	output(outFile, fmt.Sprintf("Scanning %s...", driveA))
	sizesA, _ := scanBySize(driveA)
	totalA := 0
	for _, paths := range sizesA {
		totalA += len(paths)
	}
	output(outFile, fmt.Sprintf("Found %d files in %s", totalA, driveA))

	output(outFile, fmt.Sprintf("Scanning %s...", driveB))
	sizesB, _ := scanBySize(driveB)
	totalB := 0
	for _, paths := range sizesB {
		totalB += len(paths)
	}
	output(outFile, fmt.Sprintf("Found %d files in %s", totalB, driveB))

	// Now proceed with logic — no need to recompute totals
	candidateSizes := make(map[int64]bool)
	for size := range sizesA {
		if len(sizesB[size]) > 0 {
			candidateSizes[size] = true
		}
	}

	var candidatesA, candidatesB []string
	for size, paths := range sizesA {
		if candidateSizes[size] {
			candidatesA = append(candidatesA, paths...)
		}
	}
	for size, paths := range sizesB {
		if candidateSizes[size] {
			candidatesB = append(candidatesB, paths...)
		}
	}

	hashesA := hashFiles(driveA, candidatesA)
	hashesB := hashFiles(driveB, candidatesB)

	hashSetB := make(map[string]bool)
	for _, h := range hashesB {
		hashSetB[h] = true
	}
	hashSetA := make(map[string]bool)
	for _, h := range hashesA {
		hashSetA[h] = true
	}

	var onlyA, onlyB []string
	for size, paths := range sizesA {
		if len(sizesB[size]) == 0 {
			onlyA = append(onlyA, paths...)
		} else {
			for _, path := range paths {
				if h, ok := hashesA[path]; ok {
					if !hashSetB[h] {
						onlyA = append(onlyA, path)
					}
				} else {
					onlyA = append(onlyA, path)
				}
			}
		}
	}
	for size, paths := range sizesB {
		if len(sizesA[size]) == 0 {
			onlyB = append(onlyB, paths...)
		} else {
			for _, path := range paths {
				if h, ok := hashesB[path]; ok {
					if !hashSetA[h] {
						onlyB = append(onlyB, path)
					}
				} else {
					onlyB = append(onlyB, path)
				}
			}
		}
	}

	sort.Strings(onlyA)
	sort.Strings(onlyB)

	switch mode {
	case "missing_a":
		output(outFile, fmt.Sprintf("\n=== Files missing in Tree A (%d) ===", len(onlyB)))
		for _, f := range onlyB {
			output(outFile, f)
		}
	case "missing_b":
		output(outFile, fmt.Sprintf("\n=== Files missing in Tree B (%d) ===", len(onlyA)))
		for _, f := range onlyA {
			output(outFile, f)
		}
	case "all":
		if len(onlyA) > 0 {
			output(outFile, fmt.Sprintf("\n=== Only in Tree A (%d) ===", len(onlyA)))
			for _, f := range onlyA {
				output(outFile, f)
			}
		}
		if len(onlyB) > 0 {
			output(outFile, fmt.Sprintf("\n=== Only in Tree B (%d) ===", len(onlyB)))
			for _, f := range onlyB {
				output(outFile, f)
			}
		}
	}
	return nil
}

// Helper for strict mode
func scanBySize(base string) (map[int64][]string, error) {
	groups := make(map[int64][]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

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
				rel, err := filepath.Rel(base, fullPath)
				if err != nil {
					continue
				}
				mu.Lock()
				groups[info.Size()] = append(groups[info.Size()], rel)
				mu.Unlock()
			}
		}
	}

	wg.Add(1)
	scanDir(base)
	wg.Wait()
	return groups, nil
}

// === Main run ===
func run(cmd *cobra.Command, args []string) error {
	driveA, _ := cmd.Flags().GetString("a")
	driveB, _ := cmd.Flags().GetString("b")
	mode, _ := cmd.Flags().GetString("mode")
	outPath, _ := cmd.Flags().GetString("out")
	useHashFlag, _ := cmd.Flags().GetBool("hash")
	hashMode, _ := cmd.Flags().GetString("hash-mode")

	// Resolve effective mode
	effectiveMode := "off"
	if useHashFlag {
		effectiveMode = "smart" // --hash implies smart
	}
	if hashMode != "" {
		effectiveMode = hashMode
	}

	if driveA == "" || driveB == "" {
		return fmt.Errorf("both -a and -b flags are required")
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
	var err error
	switch effectiveMode {
	case "off":
		output(outFile, "Running in 'off' mode: path+size only (no hashing).")
		output(outFile, fmt.Sprintf("Scanning %s...", driveA))
		filesA, _ := getFilesConcurrent(driveA)
		output(outFile, fmt.Sprintf("Found %d files in %s", len(filesA), driveA))

		output(outFile, fmt.Sprintf("Scanning %s...", driveB))
		filesB, _ := getFilesConcurrent(driveB)
		output(outFile, fmt.Sprintf("Found %d files in %s", len(filesB), driveB))
		compareOff(filesA, filesB, mode, outFile)
	case "smart":
		output(outFile, "Running in 'smart' mode: hashing only missing-by-path files.")
		err = compareSmart(driveA, driveB, mode, outFile)
	case "strict":
		output(outFile, "Running in 'strict' mode: global content comparison (may be slow).")
		err = compareStrict(driveA, driveB, mode, outFile)
	default:
		return fmt.Errorf("invalid hash-mode: %s (use: off, smart, strict)", effectiveMode)
	}

	if err != nil {
		return err
	}

	elapsed := time.Since(start)
	output(outFile, fmt.Sprintf("\nDone in %v (read-only scan complete).\n", elapsed))
	return nil
}

var Cmd = &cobra.Command{
	Use:   "twincheck",
	Short: "Compare two directory trees with configurable hash behavior",
	RunE:  run,
}

func init() {
	Cmd.Flags().StringP("a", "a", "", "path to Drive A (required)")
	Cmd.Flags().StringP("b", "b", "", "path to Drive B (required)")
	Cmd.Flags().StringP("mode", "m", "all", "comparison mode: all | missing_a | missing_b")
	Cmd.Flags().StringP("out", "o", "", "optional output file")
	Cmd.Flags().BoolP("hash", "H", false, "shorthand for --hash-mode=smart")
	Cmd.Flags().String("hash-mode", "off", "hashing behavior: off | smart | strict")
}
