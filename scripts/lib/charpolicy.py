"""Which characters may not be in a tracked file, or in a string that a YAML parser decoded.
One definition for scripts/check_bytes.sh (the file's bytes) and scripts/lib/yamlpolicy.py (the
decoded keys and values: a double-quoted YAML string turns the ASCII text \\L, \\N, \\_ or \\x85 into
a line break, which the byte check cannot see). Imported by path.

Refused: CR and every other control except tab and newline (Cc: C0, DEL, C1 with U+0085); format
characters (Cf: bidi controls, zero width, BOM, soft hyphen); U+2028 and U+2029; private use,
surrogates, noncharacters; any space separator other than U+0020; and the default-ignorable
characters that are not Cf either (they render as nothing, or alter their neighbours): see
IGNORABLE, which also holds the reserved (unassigned) ranges so a newer Unicode does not open a
gap. Reference for the list: INVISIBLE in web/scripts/lib/brand.mjs (Default_Ignorable_Code_Point).
Unassigned code points are not refused by category (which ones are unassigned depends on the
Python's Unicode version).

One allowance: a single U+FE0E or U+FE0F directly after a symbol (category So: the macOS emoji
picker inserts it, docs and the site use it after check marks and arrows) and not followed by
another variation selector. Anywhere else, and every other variation selector, is refused.
"""
import unicodedata

IGNORABLE = [
    (0x034F, 0x034F), (0x115F, 0x1160), (0x17B4, 0x17B5), (0x180B, 0x180F), (0x2065, 0x2065),
    (0x3164, 0x3164), (0xFE00, 0xFE0F), (0xFFA0, 0xFFA0), (0xFFF0, 0xFFF8),
    (0xE0000, 0xE0FFF),  # tags, variation selectors supplement and the reserved rest
    (0x2800, 0x2800),  # braille blank: a space that is not a space
    (0x0336, 0x0336),  # combining long stroke overlay: strikes a letter through
]


def flagged(ch, prev="", nxt=""):
    """Why `ch` is refused (prev and nxt are its neighbours), or None."""
    o = ord(ch)
    if ch in "\t\n":
        return None
    if o == 0x0D:
        return "CR (a line break to YAML, not to grep or awk)"
    if o == 0x85:
        return "NEXT LINE (a line break to YAML)"
    if o == 0x2028:
        return "LINE SEPARATOR (a line break to YAML)"
    if o == 0x2029:
        return "PARAGRAPH SEPARATOR (a line break to YAML)"
    c = unicodedata.category(ch)
    if c in ("Cc", "Cf", "Zl", "Zp", "Co", "Cs") or (c == "Zs" and ch != " "):
        return f"{c} {unicodedata.name(ch, 'unnamed')}"
    if (0xFDD0 <= o <= 0xFDEF) or (o & 0xFFFE) == 0xFFFE:
        return "noncharacter"
    if any(lo <= o <= hi for lo, hi in IGNORABLE):
        if o in (0xFE0E, 0xFE0F) and prev and unicodedata.category(prev) == "So" and not (nxt and 0xFE00 <= ord(nxt) <= 0xFE0F):
            return None
        return "default-ignorable (renders as nothing or alters the look of its neighbours)"
    return None


def findings(text):
    """[(line, char, why)] for a text, one per distinct (line, char)."""
    out, seen, line = [], set(), 1
    for i, ch in enumerate(text):
        why = flagged(ch, text[i - 1] if i else "", text[i + 1] if i + 1 < len(text) else "")
        if why and (line, ch) not in seen:
            seen.add((line, ch))
            out.append((line, ch, why))
        if ch == "\n":
            line += 1
    return out
