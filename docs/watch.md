# Watch: a bead's site, live, while it is built

A watch lets a person see a site take shape while a builder works on it. The
yard serves the files of one directory of the bead's worktree, read-only,
through the same endpoint a preview uses, and the page reloads itself when a
file changes.

```sh
yardr preview watch yardr-1a2b            # the worktree itself
yardr preview watch yardr-1a2b mockups    # one directory of it
```

```text
watching yardr-1a2b: ~/.yardr/worktrees/site/yardr-1a2b/mockups is served read-only, and its pages reload when a file changes
opens at /preview/yardr-1a2b/
ends when yardr-1a2b leaves new, after 4h without a request, or in 7d
```

The builder puts the address in the note it writes on the bead; notes show
as they always do. `yardr bead show` has nothing new. On a yard connected to an
edge the address is the host the edge gave the watch, which the command waits
a few seconds for, and the note's reader opens it in a browser:

```text
opens at https://p-12.preview.example.net/
```

Where the edge gives none, the address is the path on the yard as above, and the
words the edge refused with are printed under it
([Previews through the edge](edge.md#previews-through-the-edge)).

## A watch is a preview of another kind

A [preview](web.md#the-lists) is a program the crew built, which the yard runs
once its person has approved the command for one commit. A watch runs
nothing: the yard itself reads files it already holds. So there is nothing to
approve, and a builder who changes files every minute takes no approval back.

It is the same record. It has an address among the previews, it is listed by
`yardr preview list` (its `KIND` is `watch`, and it has no commit or port),
it is stopped with `yardr preview stop`, and the edge carries it as it
carries any preview. It takes none of the three places a yard keeps for
previews that run.

A watch serves **plain files**. A site that needs a build step (`npm run
dev`) runs the builder's code, and stays a preview with an approval:
`yardr preview propose`.

## Who starts one

The bead's builder (an agent working that bead), the user the bead is for, or
an operator. A builder cannot watch another bead's files.

## What is served

- GET and HEAD. Anything else is refused with 405.
- The directory named, or the worktree. `dir` is read against the worktree
  and must be inside it.
- A directory answers its `index.html`, and 404 where it has none: there is
  no listing. An address without its final slash is sent to the one with it.
- **Nothing whose name starts with a dot**: `.git`, `.env`, an editor's
  scratch file, and no `..` in a path. A link inside the directory that
  leads outside it, or into a dot directory, is not followed.
- Every answer is `Cache-Control: no-store`, so a reload shows the file as
  it is.

## The reload

An HTML answer, and only an HTML answer (never a script, a style sheet or
JSON), is given one small script before its `</body>`, or at its end:

```html
<script data-yardr-watch>new EventSource("/.yardr-watch/events?v=…").onmessage=function(){location.reload()}</script>
```

The script listens on the watch's event stream, `/.yardr-watch/events`.
The name starts with a dot, which serving refuses, so no file of a worktree
can be found at it. The yard reads the directory every half second, with no
file watcher, and sends one event once the directory has stood still for
300 ms after a change: a burst of writes is one reload. A change to a dot
file, such as a commit in `.git`, moves nothing.

`v` names the state of the directory the page was served from. A page
opened before a change, or a stream that was cut and reconnects, is told at
once if the directory has moved since. A directory of more than 20,000
entries (a `node_modules` among them) is read in part: it reloads late or not
at all.

## When it ends

A watch ends, and its address then says so and how to start it again, when:

- the bead leaves the stage it was in when the watch began, or is closed;
- `preview_idle` has passed with no request (four hours while unset); a page
  open in a browser holds a stream, which is a request in flight;
- it is seven days old;
- `yardr preview stop <app>` stops it (its starter or an operator).

The same `yardr preview watch <bead>` starts it again, at the same address. A
server restart, an upgrade among them, does not end a watch.

On the Previews page of the web view a running watch has a card with the bead,
the directory, its address and lifetime, and Open and Stop. An ended one
leaves the page, where a card waiting for a person's approval must stand out,
and stays in `yardr preview list`.

## Through the edge

The yard answers on its preview endpoint, `/preview/<app>/`, and the edge
carries a person's request there behind their login, on a host of its own.
The yard reports the watch to the edge as it reports any preview
([Previews through the edge](edge.md#previews-through-the-edge)): running while
it serves, stopped once it has ended, and forgotten when `yardr preview forget`
drops it. A watch binds no port, and the report says so. The host the edge
answers with is kept on the watch's row, and it stands when the same command
starts the watch again, so the address is the same. The edge serves previews
for yards on their own server (`vps`) alone today; for a yard it does not serve
it refuses the report, and the watch keeps its path on the yard.
