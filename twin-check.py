import os
from pathlib import Path

def get_files(base_path):
    """Recursively list all files with relative paths and sizes."""
    file_map = {}
    for root, _, files in os.walk(base_path):
        for name in files:
            full_path = Path(root) / name
            rel_path = full_path.relative_to(base_path)
            try:
                size = os.path.getsize(full_path)
                file_map[str(rel_path)] = size
            except (PermissionError, FileNotFoundError):
                continue
    return file_map


def compare_drives(drive_a, drive_b, mode="all"):
    """
    Compare two drives by relative path and file size.

    mode options:
        'all'        - show all differences
        'missing_a'  - show files missing in A
        'missing_b'  - show files missing in B
    """
    print(f"\nIndexing {drive_a}...")
    files_a = get_files(drive_a)
    print(f"Found {len(files_a)} files in {drive_a}")

    print(f"\nIndexing {drive_b}...")
    files_b = get_files(drive_b)
    print(f"Found {len(files_b)} files in {drive_b}")

    all_keys = set(files_a.keys()) | set(files_b.keys())

    only_in_a = []
    only_in_b = []
    different_size = []
    same = []

    print("\nComparing by relative path and size...\n")
    for rel_path in sorted(all_keys):
        size_a = files_a.get(rel_path)
        size_b = files_b.get(rel_path)

        if size_a is None:
            only_in_b.append(rel_path)
        elif size_b is None:
            only_in_a.append(rel_path)
        elif size_a != size_b:
            different_size.append(rel_path)
        else:
            same.append(rel_path)

    # Output based on mode
    if mode == "all":
        print("\n=== Files only in Drive A ===")
        for f in only_in_a: print(f)

        print("\n=== Files only in Drive B ===")
        for f in only_in_b: print(f)

        print("\n=== Files with different sizes ===")
        for f in different_size: print(f)

        print(f"\nTotal identical (same path & size): {len(same)}")
    elif mode == "missing_a":
        print("\n=== Files missing in Drive A (present in B) ===")
        for f in only_in_b: print(f)
    elif mode == "missing_b":
        print("\n=== Files missing in Drive B (present in A) ===")
        for f in only_in_a: print(f)
    else:
        print("Invalid mode.")

    # --- OPTIONAL: Delete identical files (commented out) ---
    # for f in same:
    #     try:
    #         os.remove(Path(drive_b) / f)
    #         print(f"Deleted duplicate from Drive B: {f}")
    #     except Exception as e:
    #         print(f"Error deleting {f}: {e}")

    print("\nComparison complete.")