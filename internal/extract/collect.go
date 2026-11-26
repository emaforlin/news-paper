package extract

import (
	"context"
	"errors"
	"sync"

	"github.com/emaforlin/news-paper/internal/news"
)

// Result is what Collect gathered before its cutoff.
type Result struct {
	Articles []news.Article // ranking order, len <= want
	Rejected []news.Pick    // permanent rejections seen before the cutoff
}

type outcome struct {
	art news.Article
	err error
}

// Collect extracts picks (in ranking order) until want articles succeed.
// Up to max(1, Concurrency) extractions run at once, dispatched and consumed
// in ranking order through a sliding window: a new pick is dispatched only
// when the oldest in-flight one has been consumed. Once want is reached, the
// rest is cancelled and discarded. Collect never fails; an empty Result is
// valid.
func (e *Extractor) Collect(ctx context.Context, picks []news.Pick, want int) Result {
	var res Result
	if want <= 0 || len(picks) == 0 {
		return res
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// This goroutine alone dispatches and consumes, so next needs no lock
	// and wg.Add never races wg.Wait.
	slots := make([]chan outcome, len(picks))
	var wg sync.WaitGroup
	next := 0
	dispatch := func() {
		i := next
		next++
		slots[i] = make(chan outcome, 1) // buffered: a worker never blocks
		wg.Add(1)
		go func() {
			defer wg.Done()
			art, err := e.Extract(ctx, picks[i])
			slots[i] <- outcome{art, err}
		}()
	}
	for next < min(max(1, e.Concurrency), len(picks)) {
		dispatch()
	}

	for i := range picks {
		var o outcome
		select {
		case o = <-slots[i]:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break // cancelled: record nothing more
		}
		e.record(&res, i, picks[i], o)
		if len(res.Articles) == want {
			break
		}
		if next < len(picks) {
			dispatch()
		}
	}
	cancel()
	wg.Wait() // Extract honours ctx, so in-flight work ends promptly
	return res
}

// record files one outcome. i is the 0-based ranking position.
func (e *Extractor) record(res *Result, i int, p news.Pick, o outcome) {
	log := e.log()
	var rej *Rejected
	switch {
	case o.err == nil:
		res.Articles = append(res.Articles, o.art)
		log.Info("extracted", "n", i+1, "domain", o.art.Domain(), "words", o.art.Words, "title", o.art.Title)
	case errors.As(o.err, &rej):
		res.Rejected = append(res.Rejected, p)
		log.Info("skipped", "n", i+1, "url", p.URL, "reason", rej.Reason)
	default:
		log.Warn("extract failed", "n", i+1, "url", p.URL, "err", o.err)
	}
}
