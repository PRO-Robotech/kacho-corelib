# note package testdata fixtures

Synthetic L2 "functionality" notes used by the `note` package integration
tests. They are **not** real architecture documentation — only shapes that
exercise the parser / writer.

Well-formed fixtures live under `notes/` so that `note.Load("testdata/notes")`
scans only loadable note files. The two deliberately broken fixtures live
under `badnotes/` so a `Load` of the `notes/` tree never aborts on them;
they are referenced directly by path in `Parse` tests. This `README.md`
is intentionally outside both scanned trees.

| File                            | Shape                                                          |
|----------------------------------|----------------------------------------------------------------|
| `notes/valid_with_comments.md`   | Well-formed L2 note: all keys, inline `#` comments, body text. |
| `notes/valid_minimal.md`         | Well-formed L2 note: no comments, single anchor.               |
| `notes/not_l2.md`                | Valid frontmatter but `level` is not `functionality`.          |
| `notes/generated/skip_me.md`     | Valid L2 note placed under `generated/` — must be skipped.     |
| `badnotes/malformed_yaml.md`     | `---`-delimited frontmatter whose YAML is syntactically broken.|
| `badnotes/no_frontmatter.md`     | Plain Markdown, no leading `---` block at all.                 |
