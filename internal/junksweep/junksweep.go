package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Patterns of files considered junk/temp
var deletePatterns = []string{
	"~$",      // Office temp files
	".tmp",    // Generic temp files
	".~lock.", // LibreOffice locks
	".bak",    // backup copies
	"~WRL",    // temp files from your list
	"Thumbs.db",
	".DS_Store",
}

// Checks if a file matches any of the delete patterns
func matchesDeletePattern(name string) bool {
	for _, pattern := range deletePatterns {
		if strings.Contains(name, pattern) {
			return true
		}
	}
	return false
}

// Concurrently scan directories for files to delete
func scanFilesConcurrent(baseDir string, workers int) ([]string, error) {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	dirCh := make(chan string, 100)
	fileCh := make(chan string, 1000)

	var wg sync.WaitGroup

	// Worker: only consumes dirs, scans them, sends matching files
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dir := range dirCh {
				entries, err := os.ReadDir(dir)
				if err != nil {
					continue
				}
				for _, entry := range entries {
					if entry.IsDir() {
						// Enqueue subdirs — but who does this?
						// → Not the worker! We'll do it in the feeder.
						// So we *cannot* do it here.
					} else if matchesDeletePattern(entry.Name()) {
						fileCh <- filepath.Join(dir, entry.Name())
					}
				}
			}
		}()
	}

	// Feeder: performs BFS traversal and feeds dirs to workers
	go func() {
		defer close(dirCh)

		dirs := []string{baseDir}
		for len(dirs) > 0 {
			current := dirs[0]
			dirs = dirs[1:]

			// Send to workers for file scanning
			dirCh <- current

			// Now, read it ourselves to find subdirs (to avoid worker writing to dirCh)
			entries, err := os.ReadDir(current)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() {
					dirs = append(dirs, filepath.Join(current, entry.Name()))
				}
			}
		}
	}()

	// Close fileCh when workers are done
	go func() {
		wg.Wait()
		close(fileCh)
	}()

	var files []string
	for f := range fileCh {
		files = append(files, f)
	}

	return files, nil
}

// Output files either to console or to a file
func outputFiles(files []string, outPath string) error {
	if outPath == "" {
		for _, f := range files {
			fmt.Println(f)
		}
		return nil
	}

	outFile, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	for _, f := range files {
		fmt.Fprintln(outFile, f)
	}
	return nil
}

// Delete files concurrently
func deleteFilesConcurrent(files []string, workers int) {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	fileCh := make(chan string, len(files))
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range fileCh {
				os.Remove(f) // ignore errors for now
			}
		}()
	}

	for _, f := range files {
		fileCh <- f
	}
	close(fileCh)
	wg.Wait()
}

func main() {
	dir := flag.String("dir", "", "Directory to scan for temp files (required)")
	outPath := flag.String("out", "", "Optional output file to save the list")
	workers := flag.Int("workers", 0, "Number of concurrent workers (default = NumCPU)")
	flag.Parse()

	if *dir == "" {
		fmt.Println("Error: -dir argument is required")
		flag.Usage()
		os.Exit(1)
	}

	fmt.Println("Scanning directory:", *dir)
	files, err := scanFilesConcurrent(*dir, *workers)
	if err != nil {
		fmt.Println("Error scanning directory:", err)
		os.Exit(1)
	}

	if len(files) == 0 {
		fmt.Println("No temporary or junk files found.")
		return
	}

	if err := outputFiles(files, *outPath); err != nil {
		fmt.Println("Error writing output:", err)
		os.Exit(1)
	}

	// Ask user for confirmation
	fmt.Printf("\nDo you want to delete these %d files? (Y/y/yes only to confirm): ", len(files))
	reader := bufio.NewReader(os.Stdin)
	resp, _ := reader.ReadString('\n')
	resp = strings.TrimSpace(strings.ToLower(resp))

	if resp == "y" || resp == "yes" {
		deleteFilesConcurrent(files, *workers)
		fmt.Println("Deletion complete.")
	} else {
		fmt.Println("No files were deleted.")
	}
}
