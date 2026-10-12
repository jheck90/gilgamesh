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
./gmesh ingest book.epub --dry-run  # chapters, length and number of model calls
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
docker run -d -p 8080:8080 -v gilgamesh:/data -v /path/to/books:/books:ro ghcr.io/jheck90/gilgamesh
docker exec -it -e ANTHROPIC_API_KEY=... <container> gmesh ingest /books/book.epub --dry-run
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

Photos are read on the server by [Tesseract](https://github.com/tesseract-ocr/tesseract)
(English), matched against the book's own text, and thrown away. They're
never written to disk, the database or the log, and no model sees them. The
Docker image includes Tesseract. Built from source, photo snaps need the
`tesseract` program on the PATH (`apt install tesseract-ocr`, `brew install
tesseract`). Without it everything else, typed words included, still works.

The log gets each photo snap's score, but never its text: "photo snap: 41
of 63 words read matched, found the page". A page is found when at least 4
of the words read, and at least half of them, match the book in order.

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

## Keys

Bring your own key. Reading never calls a model, so a key is only needed to
add books.

- **Anthropic:** `ANTHROPIC_API_KEY`. The default model is `claude-opus-5-5`.
  Set `--effort` (default `medium`) and `--model` on `ingest` and `edit`.
  Requests opt into server-side fallback (except on Haiku, which has none),
  so a passage a safety classifier declines is retried on a fallback model
  instead of failing.
- **Cheaper:** `ingest --model claude-haiku-5-5`, then `edit --effort high`
  on the default model. On *Children of Time* that cost about $0.90 against
  $4.25 for Opus alone, with similar cards.
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
| `internal/web` | the reading web app (server-rendered, installable) |

## License

AGPL-3.0. See [LICENSE](LICENSE).
