package twincheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// File map type
type FileMap map[string]int64

// getFilesConcurrent scans a directory concurrently and returns a map of relative paths -> sizes
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
				rel, _ := filepath.Rel(base, fullPath)
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

// output helper: either write to file or print to console
func output(outFile *os.File, s string) {
	if outFile != nil {
		fmt.Fprintln(outFile, s)
	} else {
		fmt.Println(s)
	}
}

// compareDrives compares two FileMaps by relative path and size
func compareDrives(filesA, filesB FileMap, mode string, outFile *os.File) {
	onlyA, onlyB, diffSize, same := []string{}, []string{}, []string{}, []string{}

	for path, sizeA := range filesA {
		if sizeB, ok := filesB[path]; ok {
			if sizeA != sizeB {
				diffSize = append(diffSize, path)
			} else {
				same = append(same, path)
			}
		} else {
			onlyA = append(onlyA, path)
		}
	}

	for path := range filesB {
		if _, ok := filesA[path]; !ok {
			onlyB = append(onlyB, path)
		}
	}

	// Sort slices for consistent output
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	sort.Strings(diffSize)
	sort.Strings(same)

	switch mode {
	case "all":
		output(outFile, "\n=== Only in Drive A ===")
		for _, f := range onlyA {
			output(outFile, f)
		}

		output(outFile, "\n=== Only in Drive B ===")
		for _, f := range onlyB {
			output(outFile, f)
		}

		output(outFile, "\n=== Different Size ===")
		for _, f := range diffSize {
			output(outFile, f)
		}

		output(outFile, fmt.Sprintf("\nTotal identical files (same path & size): %d", len(same)))

	case "missing_a":
		output(outFile, "\n=== Files missing in Drive A (present in B) ===")
		for _, f := range onlyB {
			output(outFile, f)
		}

	case "missing_b":
		output(outFile, "\n=== Files missing in Drive B (present in A) ===")
		for _, f := range onlyA {
			output(outFile, f)
		}

	default:
		output(outFile, "Invalid mode. Use: all | missing_a | missing_b")
	}
}

// Cmd is the cobra command for "ds twincheck"
var Cmd = &cobra.Command{
	Use:   "twincheck",
	Short: "Compare two directory trees concurrently",
	RunE:  run,
}

func init() {
	Cmd.Flags().StringP("a", "a", "", "path to Drive A (required)")
	Cmd.Flags().StringP("b", "b", "", "path to Drive B (required)")
	Cmd.Flags().StringP("mode", "m", "all", "comparison mode: all | missing_a | missing_b")
	Cmd.Flags().StringP("out", "o", "", "optional output file")
}

func run(cmd *cobra.Command, args []string) error {
	driveA, _ := cmd.Flags().GetString("a")
	driveB, _ := cmd.Flags().GetString("b")
	mode, _ := cmd.Flags().GetString("mode")
	outPath, _ := cmd.Flags().GetString("out")

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
	output(outFile, fmt.Sprintf("Scanning %s...", driveA))
	filesA, _ := getFilesConcurrent(driveA)
	output(outFile, fmt.Sprintf("Found %d files in %s", len(filesA), driveA))

	output(outFile, fmt.Sprintf("Scanning %s...", driveB))
	filesB, _ := getFilesConcurrent(driveB)
	output(outFile, fmt.Sprintf("Found %d files in %s", len(filesB), driveB))

	output(outFile, "\nComparing drives...")
	compareDrives(filesA, filesB, mode, outFile)

	elapsed := time.Since(start)
	output(outFile, fmt.Sprintf("\nComparison complete. No files were modified or deleted.\nTime taken: %s", elapsed))
	return nil
}
