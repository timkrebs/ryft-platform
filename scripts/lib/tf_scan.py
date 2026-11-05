#!/usr/bin/env python3
"""The tripwire of scripts/lib/tf_values.sh (D181): refuse a Terraform module that can run a program or read
a credential file when `terraform test` evaluates it.

  scripts/lib/tf_scan.py [--trusted F]... [--files-ok F]... [--allow-files] [--skip-dir N]... DIR   exit 0: nothing found; 1: findings (printed
                                                       as file:line: reason); 2: usage or read error

It reads every *.tf under DIR with a small HCL lexer: comments (#, //, /* */) are removed and string
and heredoc contents are blanked before the identifiers are looked for, so a token inside a comment
does not hide anything and one inside a string is not a block. It refuses

  - the identifiers provisioner, alias (an aliased provider configuration is not mocked by the test
    header, so its exec / credential_process / postrender would run), exec, config_path,
    config_paths, shared_config_files, shared_credentials_files, shared_credentials_file,
    credential_process, postrender, web_identity_token_file;
  - the string labels "local-exec", "remote-exec", data "external", data "http";
  - a \\u or \\U escape in a string (it could spell any of those labels);
  - the functions that read a local file (file, templatefile, filebase64, filemd5, filesha*, fileset,
    fileexists, pathexpand, abspath), path.cwd, path.root, any `::` function (provider::, core::) and
    data "local_file", "local_sensitive_file", "terraform_remote_state": unless the file is named by
    --files-ok (byte-identical to this checkout's, decided by the caller) or --allow-files is given
    (the caller runs Terraform in a container that sees only the work directory, or on a runner).
A --skip-dir NAME is a directory name not walked: a downloaded module's examples/ and tests/ are not
loaded by Terraform (a registry module's examples use path.cwd and the like);
  - any *.tf.json (not read by this lexer), and a file it cannot lex (an unterminated string or comment).

A --trusted FILE (byte-identical to the file of the checkout that runs the script, decided by the
caller) is skipped: infra/aws/kubernetes/providers.tf holds the real exec of `aws eks get-token`, in the
default provider configurations that the test header mocks.

This is a tripwire for an accident or a lazy attack, not a sandbox: the real boundary is a runner or a
machine without credentials (docs/OPERATIONS.md, the kind run).
"""
import os
import re
import sys

TOKENS = (
    "provisioner", "alias", "exec", "config_path", "config_paths", "shared_config_files",
    "shared_credentials_files", "shared_credentials_file", "credential_process", "postrender",
    "web_identity_token_file",
)
TOKEN_RE = re.compile(r"(?<![A-Za-z0-9_])(" + "|".join(TOKENS) + r")(?![A-Za-z0-9_])")
LABEL_RE = re.compile(r'"(local-exec|remote-exec)"|(?<![A-Za-z0-9_])data\s+"(external|http)"')
# What reads a local file into a value (an output, a log): the built-in functions (as an identifier
# followed by `(`, outside strings and comments), path.cwd / path.root, any namespaced function
# (provider::x::f(), core::file()) and the data sources that read one. A file that is byte-identical to
# this checkout's (--files-ok) or any file with --allow-files (the caller runs in a container that sees
# only the work directory) may use them.
FILE_FUNCS = (
    "file", "templatefile", "filebase64", "filemd5", "filesha1", "filesha256", "filesha512",
    "filebase64sha256", "filebase64sha512", "fileset", "fileexists", "pathexpand", "abspath",
)
FILE_RE = re.compile(r"(?<![A-Za-z0-9_-])(" + "|".join(FILE_FUNCS) + r")\s*\(|(?<![A-Za-z0-9_])(path\s*\.\s*(?:cwd|root))(?![A-Za-z0-9_])|::")
FILE_LABEL_RE = re.compile(r'(?<![A-Za-z0-9_])data\s+"(local_file|local_sensitive_file|terraform_remote_state)"')
HEREDOC_RE = re.compile(r"<<-?([A-Za-z_][A-Za-z0-9_-]*)[ \t]*\r?\n")


def lex(text):
    """Returns (code, kept, escapes): code has comments removed and strings and heredocs blanked,
    kept has comments removed and strings retained (line breaks stay in both); escapes says a \\u or
    \\U was seen in a string. Raises ValueError on an unterminated string, comment or heredoc."""
    code, kept = [], []
    escapes = False
    stack = []  # [True] a string; [False, depth] a ${ } or %{ } inside a string
    i, n = 0, len(text)

    def blank(s):
        return "".join(ch if ch in "\r\n" else " " for ch in s)

    while i < n:
        c = text[i]
        top = stack[-1] if stack else None
        if top is not None and top[0]:  # inside a string
            if c == "\\":
                two = text[i:i + 2]
                if two in ("\\u", "\\U"):
                    escapes = True
                code.append(blank(two))
                kept.append(two)
                i += 2
                continue
            if c == '"':
                stack.pop()
                code.append('"')
                kept.append('"')
                i += 1
                continue
            if text.startswith(("$${", "%%{"), i):
                code.append("   ")
                kept.append(text[i:i + 3])
                i += 3
                continue
            if text.startswith(("${", "%{"), i):
                stack.append([False, 1])
                code.append("  ")
                kept.append(text[i:i + 2])
                i += 2
                continue
            code.append(blank(c))
            kept.append(c)
            i += 1
            continue
        # code (the file's own, or inside a template)
        if c == "#" or text.startswith("//", i):
            j = text.find("\n", i)
            i = n if j < 0 else j
            continue
        if text.startswith("/*", i):
            j = text.find("*/", i + 2)
            if j < 0:
                raise ValueError("unterminated /* comment")
            code.append(blank(text[i:j + 2]))
            kept.append(blank(text[i:j + 2]))
            i = j + 2
            continue
        if c == '"':
            stack.append([True])
            code.append('"')
            kept.append('"')
            i += 1
            continue
        m = HEREDOC_RE.match(text, i)
        if m:
            tag = m.group(1)
            j = m.end()
            while True:
                if j >= n:
                    raise ValueError("unterminated heredoc " + tag)
                k = text.find("\n", j)
                line = text[j:n if k < 0 else k]
                if line.strip() == tag:
                    j = n if k < 0 else k
                    break
                j = n if k < 0 else k + 1
            code.append(blank(text[i:j]))
            kept.append(blank(text[i:j]))
            i = j
            continue
        if top is not None and not top[0]:
            if c == "{":
                top[1] += 1
            elif c == "}":
                top[1] -= 1
                if top[1] == 0:
                    stack.pop()
        code.append(c)
        kept.append(c)
        i += 1
    if stack:
        raise ValueError("unterminated string or template")
    return "".join(code), "".join(kept), escapes


def line_of(text, pos):
    return text.count("\n", 0, pos) + 1


def scan_file(path, files_ok=False):
    with open(path, "rb") as f:
        text = f.read().decode("utf-8", "replace")
    out = []
    try:
        code, kept, escapes = lex(text)
    except ValueError as e:
        return [(1, "cannot read this file (%s), refused" % e)]
    for m in TOKEN_RE.finditer(code):
        out.append((line_of(code, m.start()), "'%s' can run a program or read a credential file" % m.group(1)))
    for m in LABEL_RE.finditer(kept):
        out.append((line_of(kept, m.start()), "%s is refused" % m.group(0).strip()))
    if not files_ok:
        for m in FILE_RE.finditer(code):
            out.append((line_of(code, m.start()), "'%s' can read a local file (not this checkout's file, not in a container)" % re.sub(r"\s+", "", m.group(0))))
        for m in FILE_LABEL_RE.finditer(kept):
            out.append((line_of(kept, m.start()), "%s can read a local file" % m.group(0).strip()))
    if escapes:
        out.append((1, "a \\u or \\U escape in a string (it could spell a refused label)"))
    return out


def main(argv):
    trusted, files_ok, allow_files, skip_dirs, args = set(), set(), False, set(), []
    it = iter(argv)
    for a in it:
        if a == "--trusted":
            trusted.add(os.path.realpath(next(it, "")))
        elif a == "--files-ok":
            files_ok.add(os.path.realpath(next(it, "")))
        elif a == "--skip-dir":
            skip_dirs.add(next(it, ""))
        elif a == "--allow-files":
            allow_files = True
        else:
            args.append(a)
    if len(args) != 1 or not os.path.isdir(args[0]):
        print("usage: tf_scan.py [--trusted F]... [--files-ok F]... [--allow-files] DIR", file=sys.stderr)
        return 2
    bad = 0
    for dirpath, dirnames, filenames in os.walk(args[0]):
        dirnames[:] = [d for d in dirnames if d != ".git" and d not in skip_dirs]
        for name in sorted(filenames):
            path = os.path.join(dirpath, name)
            if name.endswith(".tf.json"):
                print("%s:1: *.tf.json is not read by this check, so it is refused" % path)
                bad += 1
            elif name.endswith(".tf"):
                if os.path.realpath(path) in trusted:
                    continue
                try:
                    found = scan_file(path, allow_files or os.path.realpath(path) in files_ok)
                except OSError as e:
                    print("tf_scan: %s: %s" % (path, e), file=sys.stderr)
                    return 2
                for ln, why in found:
                    print("%s:%d: %s" % (path, ln, why))
                    bad += 1
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
