# Vendored assets

Everything `yardr web serve` sends a browser besides HTML is in this
directory and embedded in the binary (`go:embed`, `../assets.go`): there is
no CDN, no build step and no Node. This file is not served.

| file | what | version | source | licence | sha256 |
|---|---|---|---|---|---|
| `htmx.min.js` | htmx | 2.0.11 | `dist/htmx.min.js` of https://registry.npmjs.org/htmx.org/-/htmx.org-2.0.11.tgz (the same bytes as https://unpkg.com/htmx.org@2.0.11/dist/htmx.min.js) | 0BSD | `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717` |
| `htmx-ext-sse.min.js` | htmx's server-sent events extension | 2.2.4 | `dist/sse.min.js` of https://registry.npmjs.org/htmx-ext-sse/-/htmx-ext-sse-2.2.4.tgz | 0BSD | `98a46496de0c3605fbffdce9167ba427bdd9553184f83f149c261891a92c0136` |
| `yard.css` | the stylesheet and its tokens | ours | this repository | the repository's | not pinned: it changes with the view |

The two scripts are the upstream files byte for byte. `TestVendoredAssets`
(`../web_test.go`) fails when a file's sha256 differs from this table, so a
changed script and a stale row are both caught.

## Updating a script

1. Download the package's tarball from the npm registry and copy the file
   named under "source" over the one here.
2. Check that it names no other host (`TestNoOtherHost` does).
3. Put the new version, URL and `shasum -a 256` in the table.

Nothing else is added here: one stylesheet, these two scripts.
