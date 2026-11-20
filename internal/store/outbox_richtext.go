package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/rich"
)

// richTextTarget is the destination of the html_text of an entry of kind (false if it has
// none that FitQueuedRichText fixes).
func richTextTarget(kind Kind) (rich.Target, bool) {
	switch kind {
	case OpAddComment:
		return rich.Comment, true
	case OpCreateStatus:
		return rich.StatusUpdate, true
	}
	return 0, false
}

// ErrNoFit is returned by FitQueuedRichText when there is an entry to check but no fit
// function to check it with (the Syncer was built without RichText).
var ErrNoFit = errors.New("送り待ちのコメントと status update を今の形に直す変換が渡されていません (Syncer.RichText)")

// FitQueuedRichText rewrites the html_text (and text) of queued comments and status updates
// as they are converted now (fit, richtext.Converter.Fit), and returns how many it rewrote. Before
// #63 they were converted like task notes, with headings, rules and images, which Asana
// rejects there with 400, so they could never be sent (#71). sync.Push calls it before
// sending, so such an entry is sent as it would be written now, whenever it was queued.
//
// An entry left failed is put back in the queue, to be sent again without the user having
// to, whatever made it fail (a 400, or a check in Asana that could not decide): as long as
// its html_text held those tags, Asana could not have created it, so sending it again
// cannot duplicate it. If it fails again for another reason it stays failed, and is not put
// back again, since its html_text no longer changes. An entry being sent is left alone.
//
// Without fit, it changes nothing, and returns ErrNoFit if there is an entry to check.
//
// fit rewrites rich text html as it is written for target now, and reports whether it
// changed (richtext.Converter.Fit); plain is the text for the new html.
func (s *Store) FitQueuedRichText(ctx context.Context, fit func(target rich.Target, html string) (fitted, plain string, changed bool)) (int, error) {
	fixed := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		fixed = 0
		rows, err := tx.QueryContext(ctx, `SELECT id FROM outbox WHERE kind IN (?, ?) AND state IN (?, ?)
			AND json_extract(payload, '$.html_text') LIKE '%<%'`, OpAddComment, OpCreateStatus, OpPending, OpFailed)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			op, err := getOpTx(ctx, tx, id)
			if err != nil {
				return err
			}
			target, ok := richTextTarget(op.Kind)
			if !ok {
				continue
			}
			if fit == nil {
				return ErrNoFit
			}
			changed := false
			rewrite := func(htmlText, text *string) {
				var fitted, plain string
				if fitted, plain, changed = fit(target, *htmlText); changed {
					*htmlText, *text = fitted, plain
				}
			}
			switch op.Kind {
			case OpAddComment:
				err = outbox.EditPayload(op, func(p *outbox.AddComment) { rewrite(&p.HTMLText, &p.Text) })
			case OpCreateStatus:
				err = outbox.EditPayload(op, func(p *outbox.CreateStatus) { rewrite(&p.HTMLText, &p.Text) })
			}
			if err != nil {
				return err
			}
			if !changed {
				continue
			}
			failed := op.State == OpFailed
			if failed {
				op.State, op.LastError = OpPending, ""
			}
			if err := saveOp(ctx, tx, op); err != nil {
				return err
			}
			if failed { // a fresh start, as RetryOp: nothing was sent that could be applied
				if _, err := tx.ExecContext(ctx, "UPDATE outbox SET verify = 0, attempts = 0 WHERE id = ?", id); err != nil {
					return err
				}
			}
			fixed++
		}
		return nil
	})
	return fixed, err
}
