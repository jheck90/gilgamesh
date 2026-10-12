# Gilgamesh

A self-hosted, spoiler-safe companion for the book you're reading. Tell it
where you are, and look up any character, corporation, place or term you've
met so far: just the facts the book has given you, nothing past your page.

Status: early. `gmesh` reads an EPUB, takes position-tagged notes on it
with a model, and serves a small web app you can install on your phone to
look them up, and to set where you are by snapping the page you're on. The
visual timeline and reread hints come next.

## How spoilers are kept out

A model reads the book once, in order, one chapter (or part of a chapter)
at a time, and takes notes. Every note records the sentence where the
reader learns it. Looking something up is then a database query filtered by
your position, with no model involved.

The prompt asks the model not to spoil anything, but the code doesn't trust
it to. Each note is checked before it's saved:

- **Every fact cites sentences from the passage the model was given.** A fact
  becomes visible at the last sentence it cites.
- **New names must appear in the text.** The model can't introduce someone
  by a name the book hasn't used yet, even if it knows the book.
- **Aliases must appear in the sentence they cite,** and each alias has its
  own reveal point: learning that two names are one person is a reveal too.
- **Known characters only get new facts from a passage that names them** (by
  a name you already know). This stops the model from quietly tying a scene
  to someone you haven't connected it to yet. First-person narrators are
  exempt.
- **The model sees only the current passage** plus what a reader would know
  at its start. It never sees later chapters.

Run `gmesh ingest -v` to see everything the checks drop.

### Tidying a finished book

`gmesh edit BOOK` has a model review all of a book's notes at once: drop
trivia and scene-by-scene narration (cards are a glossary, not a plot
summary), merge duplicates like "Guyen" and "Vrie Guyen", and drop time
markers like "That night". Because the editor sees the whole book, it can
only remove and combine, never write, and the code checks every change:

- **Merges need a shared name word** the reader has already seen, so a twist
  ("the hooded man was Mordecai") can't be merged. A merged card keeps its
  first appearance, and nothing folded into it shows earlier than it did.
- **Titles come from the entry's own names** and only show once the reader
  reaches that name, so "the murderer" becomes his real name at the reveal,
  not before. Without an edit, an entry first noted by a description takes
  the first proper name revealed for it.
- **Drops stay inside the book being edited,** never touch the narrator, and
  can't remove an entity with more than 5 facts.
- **The database is backed up** before anything changes.

Use `--dry-run --save plan.json` to review, then `--from plan.json` to apply
that same plan without asking the model again. Each edit trims more, so a
book that has been edited once needs `--again` to be edited again.

## Quick start

```sh
go build -o gmesh ./cmd/gmesh

export ANTHROPIC_API_KEY=...        # or see "Keys" below
./gmesh ingest book.epub --dry-run  # chapters, length, model calls and a price range
./gmesh ingest book.epub --skip 1-3,48   # leave out front matter, back matter, next-book previews
./gmesh edit book --dry-run --save plan.json   # review the tidy-up, then:
./gmesh edit book --from plan.json
./gmesh chapters book
./gmesh pos book --chapter 12       # where you are
./gmesh entities book
./gmesh card book "Princess Donut" --sources
./gmesh timeline book
```

## Run the web app

```sh
./gmesh serve --addr :8080
```

Or with Docker:

```sh
docker run -d -p 8080:8080 -v gilgamesh:/data -v /path/to/books:/books:ro \
  -e GMESH_LIBRARY=/books -e ANTHROPIC_API_KEY=... ghcr.io/jheck90/gilgamesh
```

Or as a Nomad job, with the books mounted read-only:

```hcl
task "gilgamesh" {
  driver = "docker"
  config {
    image = "ghcr.io/jheck90/gilgamesh"
    args  = ["serve", "--library", "/books"]
    ports = ["http"]
    mount {
      type     = "bind"
      source   = "/media/books"
      target   = "/books"
      readonly = true
    }
    mount {
      type   = "bind"
      source = "/srv/gilgamesh"
      target = "/data"
    }
  }
  template {
    data        = "ANTHROPIC_API_KEY={{ with nomadVar \"nomad/jobs/gilgamesh\" }}{{ .anthropic_api_key }}{{ end }}"
    destination = "secrets/env"
    env         = true
  }
}
```

Open it on your phone and use "Add to Home Screen". Tap "I finished ..."
at the end of each chapter. Partway through one, snap the page you're on:

- **Photo of your page** opens the camera. Gilgamesh finds the page in the
  book and moves you to its last whole sentence. A sentence cut off at the
  bottom counts as the next page's. If anything is revealed on the page (a
  fact, a name, someone new, a time marker), it can't know how far down
  you've read, so it shows you the page's sentences and you tap the last
  one you read, or "I haven't started this page".
- **Or type the last 4 or more words you read** and tap "Snap" to move to
  the sentence they end in.

A snap ahead of where you are moves you straight there. One behind it,
rereading an earlier page or by accident, asks first ("You're partway
through Chapter 14. Move back to Chapter 12?"), and only "Move back"
changes your position.

After a snap, the book shows where you are and, under "On this page", the
cards for everyone and everything the page names up to there, so "who is
this?" is one tap away. Someone named further down the page, or
by a name you haven't learned yet, isn't listed.

Titles of chapters you haven't reached stay hidden, and anything you
haven't met yet returns the same "Nothing here yet" as something that
doesn't exist.

Photos are turned the way the phone was held, read on the server by
[Tesseract](https://github.com/tesseract-ocr/tesseract) (English), matched against the book's own text, and thrown away. They're
never written to disk, the database or the log, and no model sees them. The
Docker image includes Tesseract. Built from source, photo snaps need the
`tesseract` program on the PATH (`apt install tesseract-ocr`, `brew install
tesseract`). Without it everything else, typed words included, still works.

The log gets each photo snap's score, but never its text: "photo snap: 41
of 63 words read matched, found the page". A page is found when at least 4
of the words read, and at least half of them, match the book in order. A
word only counts as part of 3 matched words in a row, so one misread word
can leave its neighbours unmatched too.

There are no accounts. Run it for yourself on a private network, or behind
an authenticating proxy with `--trust-header Remote-User` so each reader
gets their own position. Only use `--trust-header` when the app can't be
reached except through that proxy.

## Books

Add the books in a series in order (`--series` and `--index` override what
the EPUB says). Facts from earlier books carry forward.

Ebooks often end with a preview chapter of the next book. Check the end of
the `--dry-run` listing and skip it.

Gilgamesh only reads DRM-free EPUBs and doesn't help remove DRM.

### What a book costs

`--dry-run` ends with a price range for adding the book, on the model and
key the real run would use, and a rougher one for a later `edit`. Skipping
chapters with `--skip` lowers it. Input tokens come from the book; output
tokens follow what the same model wrote on books you've added before (a
built-in guess until then). A Claude Code key shows token counts only, since
the calls count against your Claude plan, and so does a model with no known
price.

Prices are built in. To correct one, or price a model on an
OpenAI-compatible service, point `--prices FILE` (or `GMESH_PRICES`) at a
JSON file of dollars per million tokens; models it doesn't name keep the
built-in price:

```json
{
  "claude-haiku-5-5": {"input": 0.10, "output": 0.50},
  "llama-4": {"input": 0.20, "output": 0.60}
}
```

## Keys

Bring your own key. Reading never calls a model, so a key is only needed to
add books.

- **Anthropic:** `ANTHROPIC_API_KEY`. `ingest` reads the book on
  `claude-haiku-5-5` and `edit` tidies it on `claude-opus-5-5`. On *Children
  of Time* that cost about $0.90 (with `edit --effort high`) against $4.25 for
  Opus alone, with similar cards. Set `--effort` (default `medium`) and
  `--model` on `ingest` and `edit`.
  Requests opt into server-side fallback (except on Haiku, which has none),
  so a passage a safety classifier declines is retried on a fallback model
  instead of failing.
- **A Claude subscription** (a Claude Code key): `GMESH_PROVIDER=claude-code`
  runs each call through the `claude` CLI, so adding a book counts against
  your Claude plan's usage instead of API billing. Make a token with `claude
  setup-token` and set `CLAUDE_CODE_OAUTH_TOKEN`, not `GMESH_API_KEY` (without
  one, the CLI's own login is used). Use the `ghcr.io/jheck90/gilgamesh:claude-code` image,
  which includes the CLI; built from source, `claude` must be on the PATH.
  The CLI gets no tools, settings or MCP servers, and any
  `ANTHROPIC_API_KEY` is hidden from it so it can't bill the API. If you hit
  your plan's usage limit partway through a book, `ingest` stops with the
  CLI's message; run it again after the reset to carry on. Only the admin
  can use a Claude Code key: it can't be stored per reader or borrowed.
- **Anything OpenAI-compatible** (OpenAI, OpenRouter, Ollama, llama.cpp,
  vLLM): `GMESH_PROVIDER=openai`, `GMESH_BASE_URL`, `GMESH_MODEL`, and
  `GMESH_API_KEY` if the server needs one. The model must support JSON-schema
  structured output.

Several readers can share an instance (`-user NAME`, or `GMESH_USER`).
Everyone gets their own position. To store a key per reader, set
`GMESH_SECRET` (`gmesh keygen` makes one) and run `gmesh key set`. Keys are
encrypted at rest. A reader without a key can `gmesh key request` to use the
admin's key. The first user created is the admin, and approves with
`gmesh key approve NAME --cap TOKENS` (a monthly cap; leave it off for none).

`serve` can also add books in the background, one at a time across the
instance; the rest wait in line. Each runs on the key of the reader who
started it, checked again before every model call, so revoked borrowing or a
used-up allowance pauses it with the reason and the next book starts.
"Books being added", linked from the home page, shows who started each,
which call it's on and its state. The reader who started it, or the admin,
can pause, resume or cancel it there; cancelling removes the partly added
book, as `gmesh rm` does. Killing the server loses nothing: it picks up at
the next call when it starts again. Give `serve` the same `GMESH_SECRET`
and instance key as the CLI.

### Adding a book from the web app

`serve --library DIR` (or `GMESH_LIBRARY`) names the **library**: a
folder of EPUBs, best mounted read-only. "Add a book" on the home page lists
every EPUB under it, grouped by series and number from the EPUB, with books
in no series last. Search matches title, author, series and path. Files that
can't be added stay listed, greyed out, with the reason: already added (by
content, so a copy under another path counts, linking to the book), being
added, a later book in the series is already added, can't be read (not a
valid EPUB, or no readable text, e.g. DRM), or still arriving (changed in the
last minute, so a downloader may still be writing it). Each file is read once
and remembered by its path, size and modification time; only new or changed
files are read again. Nothing outside the folder is listed or opened: paths
with `..` and symlinks that lead out are refused.

Picking a file shows its chapters with word counts and the price range, as
`ingest --dry-run` does. Tick chapters to leave out and press "Update
price"; change the series name or number if the EPUB has them wrong. A clash
with the reading order shows before anything is spent. "Then tidy its
notes" (on by default) runs `edit` afterwards, with its own estimate;
leaving it ticked agrees to that too. The model and effort are the key's
defaults (`--chunk-words` and `--model` stay CLI-only). "Add this book" is
the only step that spends: it puts the book in the queue above.

Any reader can browse the library and see prices. Without a working key
(none yet, a request the admin hasn't approved, or this month's allowance
used up), "Add this book" is disabled, with the reason and the command that
fixes it (`gmesh key set` or `gmesh key request`). `--prices` (or
`GMESH_PRICES`) works for `serve` as for `ingest`.

## Layout

| Path | What |
|---|---|
| `cmd/gmesh` | the CLI |
| `internal/epub` | EPUB parsing, chapters split on table-of-contents anchors |
| `internal/text` | sentence splitting, name matching, and finding a snapped page |
| `internal/ocr` | reading a snapped photo with the `tesseract` program |
| `internal/extract` | chunking, the prompt, and the spoiler checks (`validate.go`) |
| `internal/edit` | the whole-book tidy-up and its checks |
| `internal/store` | SQLite schema and queries; positions are sentence numbers |
| `internal/llm` | Anthropic (official Go SDK) and OpenAI-compatible clients |
| `internal/keys`, `internal/secrets` | whose key pays, and keys at rest |
| `internal/adding` | turning an EPUB into a stored book, and the background queue `serve` adds books with |
| `internal/library` | the folder of EPUBs the library page lists, and the cache of what each holds |
| `internal/estimate` | the price range before adding a book, and the built-in prices |
| `internal/web` | the reading web app (server-rendered, installable) |

## License

AGPL-3.0. See [LICENSE](LICENSE).
