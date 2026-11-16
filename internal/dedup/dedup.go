// Package dedup merges reposts (ТЗ §15, phase 4): the same post in several
// channels is exported once, with "Также в: …" naming the others. Two posts
// are the same if one forwards the other, both forward the same original
// (LOOKUP G1), or their normalised texts are equal and published within
// Window of each other. The earliest post of a group is canonical; the
// others point to it in documents.dup_of.
package dedup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"macro-collector/internal/classify"
	"macro-collector/internal/store"
)

// Window is the longest time between two posts with the same text that are
// still one repost group.
const Window = 7 * 24 * time.Hour

// MinWords is the shortest text matched by its hash: short posts ("$META
// -7%") repeat on different days without being reposts. Forwards are
// matched whatever their length.
const MinWords = 8

// Hash is the dedup hash of a text: sha256 of its normalised words, so
// links, emoji, punctuation and spacing do not matter.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(strings.Join(classify.Normalize(text), " ")))
	return hex.EncodeToString(sum[:])
}

// Stats counts what a run did.
type Stats struct {
	Hashed     int // posts that got their hash (new or edited)
	Duplicates int // posts in the checked window that repeat another one
	Changed    int // posts whose dup_of changed
}

// Run hashes new and edited posts and regroups the posts published within
// Window of them. Unchanged groups are not written, so a repeated run
// changes nothing.
func Run(ctx context.Context, st *store.Store) (Stats, error) {
	var stats Stats
	from, to, ok, err := st.UndedupedRange(ctx)
	if err != nil || !ok {
		return stats, err
	}
	// Twice the window back: a new post may join a group whose canonical
	// post is a whole window before the group's other members.
	posts, err := st.PostsPublished(ctx, from.Add(-2*Window), to.Add(Window))
	if err != nil {
		return stats, err
	}
	hashes := make([]string, len(posts))
	for i, p := range posts {
		hashes[i] = p.DedupHash
		if hashes[i] == "" {
			hashes[i] = Hash(p.Body)
		}
	}
	canon := groups(posts, hashes)

	err = st.Write(ctx, func(tx *sql.Tx) error {
		stats = Stats{}
		for i, p := range posts {
			dupOf := int64(0)
			if c := canon[i]; c != i {
				dupOf = posts[c].ID
				stats.Duplicates++
			}
			if p.DedupHash == hashes[i] && p.DupOf == dupOf {
				continue
			}
			if p.DedupHash == "" {
				stats.Hashed++
			}
			if p.DupOf != dupOf {
				stats.Changed++
			}
			if err := store.SetDedup(tx, p.ID, hashes[i], dupOf); err != nil {
				return err
			}
		}
		return nil
	})
	return stats, err
}

// groups returns, for each post, the index of the canonical post of its
// group (itself if it is canonical). posts are ordered by publication.
func groups(posts []store.DedupPost, hashes []string) []int {
	parent := make([]int, len(posts))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		// The earlier post (lower index) stays the root.
		if rb < ra {
			ra, rb = rb, ra
		}
		parent[rb] = ra
	}

	// Forwards: key "<handle>/<id>" of the original; an original in the
	// database carries the key of its own ref.
	byRef := make(map[string]int)
	for i, p := range posts {
		if h, ok := strings.CutPrefix(p.Source, "telegram:"); ok {
			byRef[strings.ToLower(h+"/"+p.ExternalID)] = i
		}
	}
	firstFwd := make(map[string]int)
	for i, p := range posts {
		if !strings.Contains(p.ForwardedFrom, "/") {
			continue // a sender name without a post link
		}
		key := strings.ToLower(p.ForwardedFrom)
		if j, ok := byRef[key]; ok {
			union(i, j)
		}
		if j, ok := firstFwd[key]; ok {
			union(i, j)
		} else {
			firstFwd[key] = i
		}
	}

	// Equal texts: consecutive posts of the same hash within Window.
	last := make(map[string]int)
	for i, p := range posts {
		if len(classify.Normalize(p.Body)) < MinWords {
			continue
		}
		if j, ok := last[hashes[i]]; ok && p.PublishedAt.Sub(posts[j].PublishedAt) <= Window {
			union(i, j)
		}
		last[hashes[i]] = i
	}

	// The root is the lowest index, i.e. the earliest post (ties by id, as
	// PostsPublished orders them).
	canon := make([]int, len(posts))
	for i := range posts {
		canon[i] = find(i)
	}
	return canon
}
