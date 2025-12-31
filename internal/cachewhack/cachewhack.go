package cachewhack

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

var (
	dryRun bool
	force  bool
	empty  bool
)

type scanRoot struct {
	path     string
	maxDepth int // 0 = unlimited, 1 = direct children only
}

// systemScanRoots defines all filesystem entry points and their scan rules.
func systemScanRoots() []scanRoot {
	var roots []scanRoot

	switch runtime.GOOS {
	case "windows":
		roots = append(roots,
			scanRoot{os.Getenv("LOCALAPPDATA"), 1}, // Tempzxpsign* lives here
			scanRoot{filepath.Join(os.Getenv("WINDIR"), "Temp"), 0},
			// scanRoot{filepath.Join(os.Getenv("WINDIR"), "SoftwareDistribution", "Download"), 0},
			scanRoot{filepath.Join(os.Getenv("LOCALAPPDATA"), "pip", "Cache"), 0},
			scanRoot{filepath.Join(os.Getenv("LOCALAPPDATA"), "npm-cache"), 0},
			scanRoot{filepath.Join(os.Getenv("LOCALAPPDATA"), "JetBrains"), 0},
			scanRoot{filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Teams", "Cache"), 0},
			scanRoot{filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "User Data"), 0},
			scanRoot{filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Edge", "User Data"), 0},
			scanRoot{filepath.Join(os.Getenv("APPDATA"), "Code", "Cache"), 0},
			scanRoot{filepath.Join(os.Getenv("APPDATA"), "Adobe"), 0},
		)

	case "darwin":
		home, _ := os.UserHomeDir()
		roots = append(roots,
			scanRoot{"/Library/Caches/Adobe", 0},
			scanRoot{filepath.Join(home, "Library", "Caches"), 0},
		)

	case "linux":
		home, _ := os.UserHomeDir()
		roots = append(roots,
			scanRoot{"/tmp", 0},
			scanRoot{"/var/tmp", 0},
			scanRoot{filepath.Join(home, ".cache"), 0},
		)
	}

	return roots
}

// matchCacheFolder reports whether a folder name matches any glob pattern.
func matchCacheFolder(name string) bool {
	name = strings.ToLower(name)

	// Explicitly ignore dangerous folders first
	if name == "package cache" || name == "slstore" {
		return false
	}

	pats := []string{
		"cache", "*cache*", "glcache", "inetcache", "webcache",
		"cacheddata", "npm-cache", "pip",
		"consentoptions", "webkit", "code cache", "gpucache",
		"bluestacks", "pypa", "squirreltemp", "go", "go-build", "vcpkg",
		// Adobe / Photoshop temp junk
		"tempzxpsign*", "photoshop temp*", "adobetemp*", "bridgecache*",
	}

	for _, p := range pats {
		if matched, _ := filepath.Match(p, name); matched {
			return true
		}
	}
	return false
}

// depth returns directory depth relative to root.
func depth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(rel, string(os.PathSeparator)))
}

// findWhackable returns absolute paths of cache folders to delete.
func findWhackable() []string {
	var out []string

	for _, sr := range systemScanRoots() {
		if sr.path == "" {
			continue
		}
		info, err := os.Stat(sr.path)
		if err != nil || !info.IsDir() {
			continue
		}

		// Root itself may be whackable
		if matchCacheFolder(filepath.Base(sr.path)) {
			out = append(out, sr.path)
			continue
		}

		_ = filepath.WalkDir(sr.path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if sr.maxDepth > 0 && depth(sr.path, path) > sr.maxDepth {
				return filepath.SkipDir
			}
			if d.IsDir() && matchCacheFolder(d.Name()) {
				out = append(out, path)
				return filepath.SkipDir
			}
			return nil
		})
	}

	return out
}

// emptyDir removes all contents of a directory without deleting the directory itself.
func emptyDir(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(path, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// whack deletes (or empties) the list concurrently.
func whack(paths []string) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if dryRun {
				fmt.Println("[dry-run] would", func() string {
					if empty {
						return "empty"
					}
					return "delete"
				}(), ":", p)
				return
			}

			var err error
			if empty {
				err = emptyDir(p)
			} else {
				err = os.RemoveAll(p)
			}

			if err != nil {
				log.Printf("failed on %s: %v", p, err)
			} else {
				log.Println("whacked:", p)
			}
		}(p)
	}
	wg.Wait()
}

// dirSize calculates total size of a directory
func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// humanSize – smart formatting with primary unit + detail in parentheses
func humanSize(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}

	const (
		_  = iota
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
		tb = gb * 1024
	)

	switch {
	case bytes >= tb:
		return fmt.Sprintf("%.1f TB (%.0f GB)", float64(bytes)/tb, float64(bytes)/gb)
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB (%.0f MB)", float64(bytes)/gb, float64(bytes)/mb)
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB (%.0f KB)", float64(bytes)/mb, float64(bytes)/kb)
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB (%d B)", float64(bytes)/kb, bytes)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func run(cmd *cobra.Command, args []string) error {
	if !force {
		dryRun = true
	}

	targets := findWhackable()
	if len(targets) == 0 {
		fmt.Println("No cache folders found to whack.")
		return nil
	}

	fmt.Printf("Found %d cache folders.\n", len(targets))

	var totalBytes int64
	for _, p := range targets {
		if dryRun {
			fmt.Printf("[dry-run] would %s : %s", func() string {
				if empty {
					return "empty"
				}
				return "delete"
			}(), p)
		}

		size, err := dirSize(p)
		if err != nil {
			if dryRun {
				fmt.Printf(" (size unknown: %v)", err)
			}
		} else {
			totalBytes += size
			if dryRun {
				fmt.Printf(" (%s)", humanSize(size))
			}
		}
		if dryRun {
			fmt.Println()
		}
	}

	if dryRun {
		fmt.Printf("\nPotential space to reclaim: %s\n", humanSize(totalBytes))
		fmt.Println("Re-run with --force to actually delete/empty.")
		return nil
	}

	fmt.Printf("\nThis will %s %d cache folders and free approximately %s of space.\n",
		func() string {
			if empty {
				return "empty"
			}
			return "delete"
		}(), len(targets), humanSize(totalBytes))
	fmt.Print("This is irreversible. Continue? (y/N): ")

	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		input := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if input != "y" && input != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	whack(targets)
	fmt.Println("System cache whack complete.")
	return nil
}

// THIS IS THE MISSING PIECE THAT FIXES YOUR COMPILER ERROR
var Cmd = &cobra.Command{
	Use:   "cachewhack",
	Short: "System-wide cache folder exterminator",
	RunE:  run,
}

func init() {
	Cmd.Flags().BoolVarP(&force, "force", "f", false, "actually delete/empty (default is dry-run)")
	Cmd.Flags().BoolVarP(&empty, "empty", "e", false, "empty folders instead of deleting them")
}

// func run(cmd *cobra.Command, args []string) error {
// 	if !force {
// 		dryRun = true
// 	}

// 	targets := findWhackable()
// 	if len(targets) == 0 {
// 		fmt.Println("No cache folders found to whack.")
// 		return nil
// 	}

// 	fmt.Printf("Found %d cache folders.\n", len(targets))

// 	if dryRun {
// 		whack(targets)
// 		fmt.Println("\nRe-run with --force to actually delete/empty.")
// 		return nil
// 	}

// 	fmt.Print("This is irreversible. Continue? (y/N): ")
// 	if sc := bufio.NewScanner(os.Stdin); sc.Scan() {
// 		if strings.ToLower(strings.TrimSpace(sc.Text())) != "y" {
// 			fmt.Println("Aborted.")
// 			return nil
// 		}
// 	}

// 	whack(targets)
// 	fmt.Println("System cache whack complete.")
// 	return nil
// }

// var Cmd = &cobra.Command{
// 	Use:   "cachewhack",
// 	Short: "System-wide cache folder exterminator",
// 	RunE:  run,
// }

// func init() {
// 	Cmd.Flags().BoolVarP(&force, "force", "f", false, "actually delete/empty (default is dry-run)")
// 	Cmd.Flags().BoolVarP(&empty, "empty", "e", false, "empty folders instead of deleting them")
// }
