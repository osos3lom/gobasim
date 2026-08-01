import os
import re
import pathlib

# Paths relative to this script location
REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]  # gobasim directory
DOCS_DIR = REPO_ROOT / 'docs'
REPORT_PATH = DOCS_DIR / 'Documentation_Code_Consistency_Report.md'

# Regular expressions to capture exported identifiers in Go source files
EXPORT_FUNC_RE = re.compile(r'\bfunc\s+([A-Z][A-Za-z0-9_]*)\b')
EXPORT_TYPE_RE = re.compile(r'\btype\s+([A-Z][A-Za-z0-9_]*)\b')
EXPORT_VAR_RE = re.compile(r'\b(var|const)\s+([A-Z][A-Za-z0-9_]*)\b')

def extract_exports():
    """Collect all exported (capitalized) symbols from .go files."""
    exports = set()
    for go_file in REPO_ROOT.rglob('*.go'):
        try:
            text = go_file.read_text(encoding='utf-8')
        except Exception:
            continue
        for m in EXPORT_FUNC_RE.finditer(text):
            exports.add(m.group(1))
        for m in EXPORT_TYPE_RE.finditer(text):
            exports.add(m.group(1))
        for m in EXPORT_VAR_RE.finditer(text):
            exports.add(m.group(2))
    return sorted(exports)

def doc_contains(term, doc_path):
    try:
        content = doc_path.read_text(encoding='utf-8')
        return term in content
    except Exception:
        return False

def generate_report(exports):
    lines = []
    lines.append('# Documentation vs Code Consistency Report')
    lines.append('')
    lines.append('Generated automatically by `scripts/check_docs_vs_code.py`.')
    lines.append('')
    total = len(exports)
    missing = []
    for term in exports:
        # Search all markdown files under docs/
        found = any(doc_contains(term, p) for p in DOCS_DIR.rglob('*.md'))
        if not found:
            missing.append(term)
    matched = total - len(missing)
    lines.append(f'**Total exported symbols:** {total}')
    lines.append(f'**Matched in documentation:** {matched}')
    lines.append(f'**Missing from documentation:** {len(missing)}')
    lines.append('')
    if missing:
        lines.append('## Missing Exported Symbols')
        lines.append('')
        for term in missing:
            lines.append(f'- `{term}`')
        lines.append('')
    else:
        lines.append('All exported symbols appear to be documented.')
        lines.append('')
    lines.append('_Note: This check only looks for exact name mentions; it does not verify the quality of the documentation._')
    REPORT_PATH.write_text('\n'.join(lines), encoding='utf-8')
    print(f'Report written to {REPORT_PATH}')

if __name__ == '__main__':
    exports = extract_exports()
    generate_report(exports)
