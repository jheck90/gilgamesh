# Gilgamesh

A spoiler-safe companion for the book a reader is reading: look up anyone or anything the book has named so far, with only the facts revealed up to the reader's position.

## Language

### Reading

**Reader**:
A person using an instance, with their own position in each book.
_Avoid_: user, account

**Position**:
The last sentence of a book the reader has read. Everything up to and including it, plus all earlier books in the series, is visible.
_Avoid_: progress, location, bookmark

**Reveal**:
The sentence at which something becomes visible: a fact, an alias, a new entity, a card title or a time marker.
_Avoid_: unlock, spoiler point

**Snap**:
Finding the reader's position from the page in front of them, by photo or by a few typed words.
_Avoid_: scan, capture, OCR (that's how, not what)

**Page**:
The run of sentences a snap matched: what the reader has in front of them, not a unit of the EPUB. A photo of two facing pages is still one page.
_Avoid_: screen, spread

**Line question**:
What a snap asks when its page holds a reveal: which of the page's sentences the reader read last, or that they haven't started the page.
_Avoid_: "line" alone for a sentence (a printed line isn't one)

### Notes

**Entity**:
A character, place, corporation or term the book names, belonging to a series so it carries across books.
_Avoid_: thing, item

**Card**:
What the reader sees for one entity: its title and the facts revealed so far.
_Avoid_: profile, page (that's a snap's)

**Fact**:
A short statement about an entity, citing the sentences it comes from; revealed at the last one.
_Avoid_: note, detail

**Alias**:
Another name for an entity, with its own reveal: learning two names are one is a reveal too.
_Avoid_: nickname

**Time marker**:
A point in the story's own timeline ("Day 1", "Year 2,000"), revealed where the book states it.
_Avoid_: date, event

### Adding books

**Library**:
The directory of EPUB files an instance can add books from. A file there is not a book until it's added.
_Avoid_: shelf, collection

**Queue**:
The books `serve` is adding in the background, one at a time across the instance, each on the key of the reader who started it. An entry is waiting, adding, editing, paused (with a reason) or done.
_Avoid_: jobs, tasks

**Key**:
What pays for the model calls that add a book: an **API key** (Anthropic or any OpenAI-compatible service) or a **Claude Code key** (a Claude subscription's login token). Reading never needs one.
_Avoid_: credential, token

**Borrowing**:
A reader using someone else's key, with the owner's approval and an optional monthly cap. A Claude Code key can't be borrowed.
_Avoid_: sharing, lending
