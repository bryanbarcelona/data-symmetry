<p align="center">
  <img src="images/data_symmetry_w_text_animated.gif" alt="Header GIF">
</p>

# Data-Symmetry

A powerful, high-performance command-line utility built in Go for maintaining order and efficiency across your file systems. **Data-Symmetry** (`ds`) provides a suite of concurrent tools for identifying junk, eliminating duplicates, and synchronizing directory trees.

-----

## ✨ Features

The tool offers four primary commands, each designed for a specific file management task:

### 1\. `junksweep`

Finds and optionally deletes common temporary and junk files from a specified directory.

  * **Target Files**: Identifies files matching patterns like Office temporary files (`~$$`), generic temporary files (`.tmp`), LibreOffice locks (`.~lock.`), backup copies (`.bak`), and system files like `Thumbs.db` and `.DS_Store`.

### 2\. `twincheck`

Compares the contents of two directory trees to identify differences and discrepancies. Read-only — never modifies files.

  * **Hash modes** (`--hash-mode`): Controls hashing behavior:
      * **`off`** *(default)*: Compares files by **path and size only**. Fast, no hashing.
      * **`smart`**: Hashes only files that are **missing-by-path** in one tree. Good balance of speed and confidence.
      * **`strict`**: Full content hash of every file in both trees. Catches renames and moves.
  * **Report modes** (`--mode` / `-m`): Controls what to display:
      * **`all`** *(default)*: Show all differences.
      * **`missing_a`**: Show only files missing from tree A.
      * **`missing_b`**: Show only files missing from tree B.
  * **`-H`**: Shorthand for `--hash-mode=smart`.
  * **`--case-sensitive`**: Enable case-sensitive path comparison. Default is case-insensitive, which is correct for most phone/camera exports vs desktop comparisons.

### 3\. `dupekill`

Removes duplicate files from one or more cleanup directories. Supports two modes of operation:

  * **Reference + Cleanup**: Files in `--cleanup` that also exist in `--reference` are flagged for removal. The reference tree is never modified.
  * **Internal dedupe**: Pass `--cleanup` alone (no `--reference`) to deduplicate files within a single folder. The alphabetically first path in each duplicate group is kept.

  **Duplicate matching modes** (`--mode`):
  * **`path`**: Same relative path only. Fast but unsafe unless directory structures are identical.
  * **`path+size`**: Same relative path and file size.
  * **`filename+size`**: Same filename and file size, regardless of subdirectory.
  * **`path+hash`**: Same relative path and content hash.
  * **`hash`** *(default)*: Same content hash only. Most thorough — catches renamed or moved duplicates.

  **Hashing optimization flags** (applies to `hash` and `path+hash` modes):
  * **`--partial-hash`**: Instead of reading entire files, hashes three 1 MB segments: the first 1 MB (header), 1 MB from the exact midpoint (center), and the last 1 MB (footer). Files under 3 MB are fully hashed automatically. Dramatically faster for large media collections.
  * **`--unsafe`**: Requires `--partial-hash`. Skips the full-hash verification pass that runs after partial matches. Near-zero false-positive risk for photos and videos (EXIF headers and compressed frame data at the midpoint are unique per file), but not recommended for arbitrary file types.

  In `hash` mode, a **size pre-filter** always runs first: only files whose size appears in both trees are ever read or hashed. Unique-sized files are skipped entirely.

### 4\. `cachewhack`

System-wide cache-folder exterminator. Discovers and safely deletes (or empties) application and OS cache directories, freeing disk space without harming user data.

  * **Auto-Discovery**: Chrome, Edge, VS Code, npm, pip, JetBrains IDEs, Adobe, OS temp folders, etc.
  * **Cross-Platform**: Windows (`%LOCALAPPDATA%`, `%WINDIR%\Temp`), macOS (`~/Library/Caches`), Linux (`/tmp`, `~/.cache`).
  * **Safety First**: Dry-run by default, depth-limited scanning, deny-list protection, interactive confirmation.
  * **Flexible**: `--empty` flag wipes contents while preserving folder structure; concurrent workers for speed.

-----

## 💻 Installation

### Pre-built binaries (recommended)

Download the latest release for your OS and architecture from the [Releases page](https://github.com/bryanbarcelona/data-symmetry/releases). Extract and place `ds` (or `ds.exe` on Windows) somewhere on your `PATH`.

### From source

Requires Go 1.21+.

```bash
go install github.com/bryanbarcelona/data-symmetry/cmd/ds@latest
```

The binary will be installed as `ds` in your `$GOPATH/bin`. Make sure that directory is on your `PATH`.

-----

## 🚀 Usage

The main command is `ds`, followed by the desired sub-command.

### `ds junksweep` Examples

```bash
# Scan a directory for junk files (dry-run)
ds junksweep --dir /path/to/my-photo-archive

# Scan and save the list to an output file
ds junksweep -d /path/to/my-photo-archive -o junk_files.txt
```

### `ds twincheck` Examples

```bash
# Fast comparison: path + size only (no hashing)
ds twincheck -a /drive/a -b /drive/b

# Smart: hash only files missing by path between the two trees
ds twincheck -a /backup/data -b /live/data -H

# Strict: full content hash of every file in both trees
ds twincheck -a /master/disk -b /clone/disk --hash-mode strict -o report.txt

# Case-sensitive comparison (default is case-insensitive)
ds twincheck -a /photos/old-phone -b /photos/new-phone -H --case-sensitive
```

### `ds dupekill` Examples

```bash
# Delete files in /cleanup/ that are duplicates (by content hash) of files in /reference/
ds dupekill --reference /path/to/reference --cleanup /path/to/cleanup

# Use multiple cleanup directories
ds dupekill --reference /master/photos --cleanup /photos/unsorted --cleanup /photos/old

# Internal dedupe: remove duplicates within a single folder (keeps alphabetically first)
ds dupekill --cleanup /path/to/folder --mode hash

# Fast mode for large media libraries: partial hash + size pre-filter + full verify
ds dupekill --reference /old-phone --cleanup /new-phone --partial-hash

# Fastest mode for trusted sources (e.g. verifying a phone transfer):
# skips full-hash verify, safe for photos/videos
ds dupekill --reference /old-phone --cleanup /new-phone --partial-hash --unsafe

# Match by filename + size instead of content (useful when content is guaranteed identical)
ds dupekill --reference /master --cleanup /incoming --mode filename+size

# Move duplicates to a folder instead of deleting them
ds dupekill --reference /master --cleanup /archive --move-to /trash/dupes
```

### `ds cachewhack` Examples

```bash
# Dry-run: see what would be deleted and how much space is reclaimable
ds cachewhack

# Actually delete the discovered cache folders (prompts for confirmation)
ds cachewhack --force

# Empty folders instead of deleting them (keeps directory structure)
ds cachewhack -f -e
```

For full flag details on any command:

```bash
ds <command> --help
```

-----

## 🤝 Contributing

Contributions are welcome\! Please feel free to open an issue or submit a pull request.

-----

## 📄 License

This project is licensed under the **MIT License**.

---

## 🤖 AI Usage Declaration

Congratulations! You've officially found the part of this README that is legally obligated to tell you: yes, a robot helped write this.

Every word has been personally vetted by me, your fearless human editor, in between existential debates with my In-N-Out burger. The robot did the typing, sure, but the seasoning? That's all human. So rest easy: this README is 100% human-approved, slightly onion-scented, and may cause sudden urges to visit fast-food joints.

---

© 2025 Bryan Barcelona. All rights reserved.
