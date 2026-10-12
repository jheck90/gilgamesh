package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jheck90/gilgamesh/internal/edit"
	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
)

func (a *app) edit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	model := fs.String("model", "", "model to use instead of the key's default")
	effort := fs.String("effort", "medium", "Anthropic effort: low, medium, high, xhigh or max")
	dry := fs.Bool("dry-run", false, "show what would change without changing anything")
	save := fs.String("save", "", "also write the model's reply to FILE, to apply later with --from")
	from := fs.String("from", "", "apply a reply saved with --save instead of asking the model again")
	verbose := fs.Bool("v", false, "list every fact and time marker dropped")
	again := fs.Bool("again", false, "edit a book that has been edited before (each edit trims more)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: gmesh edit BOOK [--dry-run] [--save FILE | --from FILE]")
	}
	book, err := a.book(ctx, pos[0])
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	user, err := a.user(ctx)
	if err != nil {
		return err
	}
	if p, err := st.Progress(ctx, book.ID); err != nil {
		return err
	} else if !p.Done() {
		return fmt.Errorf("%s isn't fully read yet (%d of %d calls); finish `gmesh ingest` first", book.Title, p.NextChunk, p.TotalChunks)
	}
	if at, edited, err := st.LastEdit(ctx, book.ID); err != nil {
		return err
	} else if edited && !*again {
		return fmt.Errorf("%s was already edited (%s UTC); each edit trims more, so add --again if you mean it", book.Title, at)
	}
	notes, err := st.BookNotes(ctx, book)
	if err != nil {
		return err
	}
	nFacts := 0
	for _, e := range notes.Entities {
		nFacts += len(e.Facts)
	}

	var raw []byte
	var usage *store.UsageRec
	planner := "saved plan " + *from
	if *from != "" {
		if raw, err = os.ReadFile(*from); err != nil {
			return err
		}
	} else {
		client, res, err := a.modelClient(ctx, st, user, *model, llm.DefaultAnthropicModel, *effort)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "reviewing %d entities, %d facts and %d time markers in %s\n", len(notes.Entities), nFacts, len(notes.Markers), book.Title)
		started := time.Now()
		var used llm.Usage
		raw, used, err = (&edit.Editor{LLM: client}).Ask(ctx, notes)
		usage = &store.UsageRec{UserID: user.ID, KeyOwnerID: res.OwnerID, Provider: client.Provider(), Model: client.Model(),
			InputTokens: used.InputTokens, OutputTokens: used.OutputTokens}
		planner = client.Model()
		if err != nil {
			a.recordUsage(ctx, st, book, usage)
			return err
		}
		fmt.Fprintf(a.out, "%d in / %d out tokens · %s\n", used.InputTokens, used.OutputTokens, time.Since(started).Round(time.Second))
		if *save != "" {
			if err := os.WriteFile(*save, raw, 0o600); err != nil {
				return err
			}
		}
	}

	plan, err := edit.Check(notes, raw)
	if err != nil {
		a.recordUsage(ctx, st, book, usage)
		return err
	}
	plan.Edit.Usage = usage
	plan.Edit.Model = planner
	a.showPlan(plan, nFacts, len(notes.Markers), *verbose)

	if *dry {
		a.recordUsage(ctx, st, book, usage)
		fmt.Fprintln(a.out, "dry run: nothing changed")
		return nil
	}
	base := fmt.Sprintf("%s.before-edit-%d-%s", a.dbPath, book.ID, time.Now().Format("20060102-150405"))
	backup := base
	for i := 2; ; i++ {
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			break
		}
		backup = fmt.Sprintf("%s-%d", base, i)
	}
	if err := st.Backup(ctx, backup); err != nil {
		return fmt.Errorf("backing up before the edit: %w", err)
	}
	s, err := st.ApplyEdit(ctx, plan.Edit)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "done: %d entities merged, %d retitled, %d dropped, %d aliases, %d facts and %d time markers dropped, %d entities recategorized\n",
		s.Merged, s.Titled, s.Entities, s.Aliases, s.Facts, s.Markers, s.Retyped)
	fmt.Fprintf(a.out, "the database before this edit is in %s\n", backup)
	return nil
}

// recordUsage saves the tokens a model call spent when nothing else will.
func (a *app) recordUsage(ctx context.Context, st *store.Store, b store.Book, u *store.UsageRec) {
	if u == nil {
		return
	}
	if _, err := st.ApplyEdit(context.WithoutCancel(ctx), store.Edit{BookID: b.ID, SeriesID: b.SeriesID, Usage: u}); err != nil {
		fmt.Fprintln(a.out, "couldn't record token usage:", err)
	}
}

func (a *app) showPlan(p edit.Plan, nFacts, nMarkers int, verbose bool) {
	list := func(title string, items []string, limit int) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(a.out, "\n%s (%d):\n", title, len(items))
		for i, it := range items {
			if limit > 0 && i == limit {
				fmt.Fprintf(a.out, "  … and %d more (-v lists them all)\n", len(items)-limit)
				break
			}
			fmt.Fprintf(a.out, "  %s\n", it)
		}
	}
	limit := 15
	if verbose {
		limit = 0
	}
	list("Categories folded", p.Retype, 0)
	list("Merges", p.Merges, 0)
	list("Titles", p.Titles, 0)
	list("Entities dropped", p.Entities, 0)
	list("Aliases dropped", p.Aliases, 0)
	list(fmt.Sprintf("Facts dropped, of %d", nFacts), p.Facts, limit)
	list(fmt.Sprintf("Time markers dropped, of %d", nMarkers), p.Markers, limit)
	list("Not allowed, left as they were", p.Rejected, 0)
	fmt.Fprintln(a.out)
}
