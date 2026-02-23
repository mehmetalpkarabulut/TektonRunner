#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
fix-encoding.sh - ISO-8859-9/CP1254 and mojibake cleanup to UTF-8

Usage:
  scripts/fix-encoding.sh [ROOT] [--dry-run] [--no-backup]

Arguments:
  ROOT         Root directory to scan (default: current directory)

Options:
  --dry-run    Report what would be changed without writing files
  --no-backup  Do not create .bak-encoding backups

What it does:
  1) Converts charset=iso-8859-9 / windows-1254 / iso-8859-1 files to UTF-8
  2) Repairs invalid UTF-8 files with iconv UTF-8 -> UTF-8//IGNORE
  3) Cleans common double-encoded Turkish sequences
EOF
}

ROOT="."
DRY_RUN=0
BACKUP=1

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --no-backup) BACKUP=0 ;;
    -h|--help) usage; exit 0 ;;
    *)
      if [[ "$ROOT" == "." ]]; then
        ROOT="$arg"
      else
        echo "Unknown argument: $arg" >&2
        usage
        exit 2
      fi
      ;;
  esac
done

if [[ ! -d "$ROOT" ]]; then
  echo "Root directory not found: $ROOT" >&2
  exit 2
fi

tmp_report="$(mktemp)"
trap 'rm -f "$tmp_report"' EXIT

scanned=0
converted=0
repaired=0
moji_fixed=0

is_text_mime() {
  case "$1" in
    text/*|application/json*|application/xml*|application/javascript*|application/x-shellscript*|application/x-yaml*|application/yaml*) return 0 ;;
    *) return 1 ;;
  esac
}

backup_file() {
  local f="$1"
  if [[ "$BACKUP" -eq 1 && ! -e "${f}.bak-encoding" ]]; then
    cp -a "$f" "${f}.bak-encoding"
  fi
}

fix_mojibake_file() {
  local f="$1"
  local before after
  before="$(sha256sum "$f" | awk '{print $1}')"

  if [[ "$DRY_RUN" -eq 1 ]]; then
    perl -CS -pe '
      s/Ã§/ç/g; s/Ã‡/Ç/g; s/ÄŸ/ğ/g; s/Äž/Ğ/g; s/Ä°/İ/g; s/Ä±/ı/g;
      s/Ã¶/ö/g; s/Ã–/Ö/g; s/Ã¼/ü/g; s/Ãœ/Ü/g; s/ÅŸ/ş/g; s/Åž/Ş/g;
      s/â€™/’/g; s/â€œ/“/g; s/â€/”/g; s/â€“/–/g; s/â€”/—/g; s/â€¦/…/g;
      s/Â//g;
    ' "$f" >/dev/null
  else
    perl -CS -i -pe '
      s/Ã§/ç/g; s/Ã‡/Ç/g; s/ÄŸ/ğ/g; s/Äž/Ğ/g; s/Ä°/İ/g; s/Ä±/ı/g;
      s/Ã¶/ö/g; s/Ã–/Ö/g; s/Ã¼/ü/g; s/Ãœ/Ü/g; s/ÅŸ/ş/g; s/Åž/Ş/g;
      s/â€™/’/g; s/â€œ/“/g; s/â€/”/g; s/â€“/–/g; s/â€”/—/g; s/â€¦/…/g;
      s/Â//g;
    ' "$f"
  fi

  after="$(sha256sum "$f" | awk '{print $1}')"
  if [[ "$before" != "$after" ]]; then
    moji_fixed=$((moji_fixed + 1))
    printf 'MOJIBAKE_FIXED\t%s\n' "$f" >>"$tmp_report"
  fi
}

while IFS= read -r -d '' f; do
  # Skip backup files created by this tool
  [[ "$f" == *.bak-encoding ]] && continue
  [[ "$f" == */scripts/fix-encoding.sh ]] && continue

  info="$(file -bi "$f" 2>/dev/null || true)"
  mime="${info%%;*}"
  charset="$(printf '%s' "$info" | sed -n 's/.*charset=\([^;]*\).*/\1/p')"
  [[ -z "$charset" ]] && continue
  is_text_mime "$mime" || continue

  scanned=$((scanned + 1))

  case "$charset" in
    iso-8859-9|windows-1254|iso-8859-1|unknown-8bit)
      from="$charset"
      [[ "$charset" == "windows-1254" ]] && from="CP1254"
      [[ "$charset" == "unknown-8bit" ]] && from="ISO-8859-9"
      if [[ "$DRY_RUN" -eq 1 ]]; then
        if iconv -f "$from" -t UTF-8 "$f" >/dev/null 2>&1; then
          converted=$((converted + 1))
          printf 'WOULD_CONVERT_%s_TO_UTF8\t%s\n' "$from" "$f" >>"$tmp_report"
        fi
      else
        tmp="$(mktemp)"
        if iconv -f "$from" -t UTF-8 "$f" >"$tmp" 2>/dev/null; then
          backup_file "$f"
          mv "$tmp" "$f"
          converted=$((converted + 1))
          printf 'CONVERTED_%s_TO_UTF8\t%s\n' "$from" "$f" >>"$tmp_report"
        else
          rm -f "$tmp"
        fi
      fi
      ;;
  esac

  # Validate/repair UTF-8 files
  info_after="$(file -bi "$f" 2>/dev/null || true)"
  if printf '%s' "$info_after" | grep -q 'charset=utf-8'; then
    if ! iconv -f UTF-8 -t UTF-8 "$f" >/dev/null 2>&1; then
      if [[ "$DRY_RUN" -eq 1 ]]; then
        if iconv -f UTF-8 -t UTF-8//IGNORE "$f" >/dev/null 2>&1; then
          repaired=$((repaired + 1))
          printf 'WOULD_REPAIR_INVALID_UTF8\t%s\n' "$f" >>"$tmp_report"
        fi
      else
        tmp="$(mktemp)"
        if iconv -f UTF-8 -t UTF-8//IGNORE "$f" >"$tmp" 2>/dev/null; then
          backup_file "$f"
          mv "$tmp" "$f"
          repaired=$((repaired + 1))
          printf 'REPAIRED_INVALID_UTF8\t%s\n' "$f" >>"$tmp_report"
        else
          rm -f "$tmp"
        fi
      fi
    fi
    fix_mojibake_file "$f"
  fi
done < <(find "$ROOT" \
  -name .git -prune -o \
  -name .cache -prune -o \
  -name .codex -prune -o \
  -path '*/harbor/data' -prune -o \
  -type f -print0)

echo "SCANNED_TEXT_FILES=$scanned"
echo "CONVERTED_TO_UTF8=$converted"
echo "REPAIRED_INVALID_UTF8=$repaired"
echo "MOJIBAKE_FIXED=$moji_fixed"

if [[ -s "$tmp_report" ]]; then
  echo
  echo "Changes:"
  cat "$tmp_report"
fi
