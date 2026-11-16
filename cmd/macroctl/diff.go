package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"macro-collector/internal/app"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/store"
	"macro-collector/internal/textdiff"
)

func diffCmd(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	typ := fs.String("type", "", "fomc | ecb")
	latest := fs.Bool("latest", false, "compare the two latest statements (ecb: decisions and statement introductions)")
	a := fs.String("a", "", "old document: id or external id (monetary20260729a, ecb.mp260910~314e508016)")
	b := fs.String("b", "", "new document: id or external id")
	_ = fs.Parse(args)
	switch *typ {
	case "fomc", "ecb":
	default:
		return fmt.Errorf("--type must be fomc or ecb, got %q", *typ)
	}
	if *latest == (*a != "" || *b != "") || (*a == "") != (*b == "") {
		return errors.New("use either --latest or both --a and --b")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if *typ == "ecb" {
		return diffECB(ctx, env, st, *latest, *a, *b)
	}

	var oldDoc, newDoc model.Document
	if *latest {
		docs, err := st.LatestDocuments(ctx, fedtexts.Source, fedtexts.DocStatement, 2)
		if err != nil {
			return err
		}
		if len(docs) < 2 {
			return fmt.Errorf("need two FOMC statements, have %d (run: macroctl fetch --source fed)", len(docs))
		}
		newDoc, oldDoc = docs[0], docs[1]
	} else {
		if oldDoc, err = statementByRef(ctx, st, *a); err != nil {
			return err
		}
		if newDoc, err = statementByRef(ctx, st, *b); err != nil {
			return err
		}
	}

	et := env.Config.Calendar.ReleaseLocation
	fmt.Println("# FOMC statement — изменения")
	fmt.Println()
	for _, x := range []struct {
		label string
		d     model.Document
	}{{"Было", oldDoc}, {"Стало", newDoc}} {
		fmt.Printf("- %s: %s ET — %s (id %d, %s) — %s\n", x.label, x.d.PublishedAt.In(et).Format("2006-01-02 15:04"),
			x.d.Title, x.d.ID, x.d.ExternalID, x.d.URL)
	}
	fmt.Println()
	fmt.Print(textdiff.Compare(oldDoc.Body, newDoc.Body).Markdown())
	return nil
}

func statementByRef(ctx context.Context, st *store.Store, ref string) (model.Document, error) {
	d, ok, err := st.Document(ctx, fedtexts.Source, ref)
	if err != nil {
		return model.Document{}, err
	}
	if !ok {
		return model.Document{}, fmt.Errorf("document %q not found", ref)
	}
	if d.DocType != fedtexts.DocStatement {
		return model.Document{}, fmt.Errorf("document %q is a %s, not an FOMC statement", ref, d.DocType)
	}
	return d, nil
}

// diffECB prints the comparison of ECB documents: with latest, the two
// latest decision releases and the two latest statement introductions;
// otherwise a and b, which must be of the same type.
func diffECB(ctx context.Context, env *app.Env, st *store.Store, latest bool, a, b string) error {
	if !env.Config.ECBTexts.Enabled {
		return errors.New("ecb_texts is not enabled in the config")
	}
	loc := env.Config.ECBTexts.Location
	print := func(title string, oldDoc, newDoc model.Document) {
		fmt.Printf("# %s — изменения\n\n", title)
		for _, x := range []struct {
			label string
			d     model.Document
		}{{"Было", oldDoc}, {"Стало", newDoc}} {
			fmt.Printf("- %s: %s — %s (id %d, %s) — %s\n", x.label, x.d.PublishedAt.In(loc).Format("2006-01-02 15:04 MST"),
				x.d.Title, x.d.ID, x.d.ExternalID, x.d.URL)
		}
		fmt.Println()
		fmt.Print(textdiff.Compare(oldDoc.Body, newDoc.Body).Markdown())
	}
	if !latest {
		oldDoc, err := ecbDocByRef(ctx, st, a)
		if err != nil {
			return err
		}
		newDoc, err := ecbDocByRef(ctx, st, b)
		if err != nil {
			return err
		}
		if oldDoc.DocType != newDoc.DocType {
			return fmt.Errorf("documents differ in type: %s and %s", oldDoc.DocType, newDoc.DocType)
		}
		print(ecbTitles[oldDoc.DocType], oldDoc, newDoc)
		return nil
	}
	for i, typ := range []string{ecbtexts.DocDecision, ecbtexts.DocStatement} {
		docs, err := st.LatestDocuments(ctx, ecbtexts.Source, typ, 2)
		if err != nil {
			return err
		}
		if len(docs) < 2 {
			return fmt.Errorf("need two %s documents, have %d (run: macroctl fetch --source ecb_texts)", typ, len(docs))
		}
		if i > 0 {
			fmt.Println()
		}
		print(ecbTitles[typ], docs[1], docs[0])
	}
	return nil
}

var ecbTitles = map[string]string{
	ecbtexts.DocDecision: "ЕЦБ: решение (Monetary policy decisions)", ecbtexts.DocStatement: "ЕЦБ: заявление (вступление пресс-конференции)",
	ecbtexts.DocAccounts: "ЕЦБ: accounts", ecbtexts.DocPressConference: "ЕЦБ: Q&A пресс-конференции",
}

func ecbDocByRef(ctx context.Context, st *store.Store, ref string) (model.Document, error) {
	d, ok, err := st.Document(ctx, ecbtexts.Source, ref)
	if err != nil {
		return model.Document{}, err
	}
	if !ok {
		return model.Document{}, fmt.Errorf("document %q not found", ref)
	}
	if ecbTitles[d.DocType] == "" {
		return model.Document{}, fmt.Errorf("document %q is a %s: diff compares decisions, statements, accounts or Q&A", ref, d.DocType)
	}
	return d, nil
}
