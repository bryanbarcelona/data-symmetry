# Changelog

All notable changes follow [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)
and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2025-11-02
### Added
- New single-binary CLI `ds` (data-symmetry) providing two sub-commands:
  - `ds junksweep -d <dir> [-o list.txt] [-w workers]`  
    Recursively finds temporary/junk files (`~$*`, `*.tmp`, `Thumbs.db`, etc.),  
    lists them, and optionally deletes them after interactive confirmation  
    (accepts `y` or `yes`).
  - `ds twincheck -a <dirA> -b <dirB> [-m mode] [-o report.txt]`  
    Concurrently walks both directory trees, compares relative paths + sizes,  
    and reports:  
      - files only in A or only in B  
      - files with differing sizes  
      - count of identical files  
    Modes: `all` (default), `missing_a`, `missing_b`.
- Cross-platform support: Windows, macOS, Linux (amd64 + arm64).
- Embedded version string (`ds --version`).
- GitHub Actions CI pipeline: test + build + release automation.

### Security note
No files are modified or deleted unless the user explicitly confirms deletion in `junksweep`.

[0.1.0]: https://github.com/bryanbarcelona/data-symmetry/releases/tag/v0.1.0