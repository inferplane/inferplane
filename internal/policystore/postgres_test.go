package policystore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// newTestStore skips unless INFERPLANE_TEST_PG_DSN is set — CI stays
// database-free — and truncates both tables on cleanup.
func newTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("INFERPLANE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("INFERPLANE_TEST_PG_DSN not set")
	}
	p, err := NewPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_ = p.ensureSchema(ctx)
		_, _ = p.db.Exec(ctx, `TRUNCATE policies, policy_meta`)
		p.Close()
	})
	return p
}

func TestPutListRoundTrip(t *testing.T) {
	p := newTestStore(t)
	ctx := context.Background()

	// Insert out of name order to prove List's ORDER BY name.
	if err := p.Put(ctx, "team-b", []byte("doc: b")); err != nil {
		t.Fatal(err)
	}
	if err := p.Put(ctx, "team-a", []byte("doc: a")); err != nil {
		t.Fatal(err)
	}
	docs, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("List returned %d docs, want 2", len(docs))
	}
	if docs[0].Name != "team-a" || docs[1].Name != "team-b" {
		t.Fatalf("List not ordered by name: %q, %q", docs[0].Name, docs[1].Name)
	}
	if !bytes.Equal(docs[0].YAML, []byte("doc: a")) || !bytes.Equal(docs[1].YAML, []byte("doc: b")) {
		t.Fatalf("List round trip corrupted bytes: %q, %q", docs[0].YAML, docs[1].YAML)
	}
	if docs[0].UpdatedAt.IsZero() || docs[1].UpdatedAt.IsZero() {
		t.Fatal("List returned a zero updated_at")
	}
}

func TestPutUpsert(t *testing.T) {
	p := newTestStore(t)
	ctx := context.Background()

	if err := p.Put(ctx, "team-a", []byte("v: 1")); err != nil {
		t.Fatal(err)
	}
	docs, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := docs[0].UpdatedAt

	if err := p.Put(ctx, "team-a", []byte("v: 2")); err != nil {
		t.Fatal(err)
	}
	docs, err = p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("upsert produced %d rows, want 1", len(docs))
	}
	if !bytes.Equal(docs[0].YAML, []byte("v: 2")) {
		t.Fatalf("second Put did not replace the document: %q", docs[0].YAML)
	}
	if docs[0].UpdatedAt.Before(first) {
		t.Fatalf("updated_at went backwards: %v before %v", docs[0].UpdatedAt, first)
	}
}

func TestDeleteNotFound(t *testing.T) {
	p := newTestStore(t)
	ctx := context.Background()

	if err := p.Put(ctx, "team-a", []byte("doc: a")); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, "team-a"); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, "team-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete: got %v, want ErrNotFound", err)
	}
	if err := p.Delete(ctx, "never-existed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete of an absent name: got %v, want ErrNotFound", err)
	}
}

func TestSeedMarkerGated(t *testing.T) {
	p := newTestStore(t)
	ctx := context.Background()

	seeded, err := p.Seeded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("fresh store reports seeded")
	}

	did, err := p.Seed(ctx, []Doc{
		{Name: "team-a", YAML: []byte("doc: a")},
		{Name: "team-b", YAML: []byte("doc: b")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("first Seed reported (false, nil)")
	}

	seeded, err = p.Seeded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !seeded {
		t.Fatal("Seeded false after Seed")
	}

	// Delete a document, then Seed again: the MARKER gates it, so the second
	// Seed is a no-op and must not resurrect the deleted document.
	if err := p.Delete(ctx, "team-a"); err != nil {
		t.Fatal(err)
	}
	did, err = p.Seed(ctx, []Doc{{Name: "team-a", YAML: []byte("doc: a")}})
	if err != nil {
		t.Fatal(err)
	}
	if did {
		t.Fatal("second Seed re-seeded an already-seeded store")
	}
	docs, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Name != "team-b" {
		t.Fatalf("second Seed resurrected a deleted document: %+v", docs)
	}
}

func TestSeedEmptyStillSetsMarker(t *testing.T) {
	p := newTestStore(t)
	ctx := context.Background()

	did, err := p.Seed(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("Seed with no docs did not seed")
	}
	seeded, err := p.Seeded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !seeded {
		t.Fatal("empty Seed did not set the marker")
	}
}

// Construction must never dial: an unreachable DSN cannot fail construction.
// Runs without a database.
func TestNewPostgresIsLazy(t *testing.T) {
	start := time.Now()
	p, err := NewPostgres("postgres://user:secretpw@203.0.113.1:5432/nope?connect_timeout=1")
	if err != nil {
		t.Fatalf("lazy construction must not dial: %v", err)
	}
	defer p.Close()
	if time.Since(start) > time.Second {
		t.Fatal("construction blocked on a dial")
	}
}

// The DSN (which may carry a password) must never appear in a constructor
// error. Runs without a database.
func TestNewPostgresDSNNeverInErrors(t *testing.T) {
	dsn := "host=127.0.0.1 password=secretpw port=notaport"
	_, err := NewPostgres(dsn)
	if err == nil {
		t.Fatal("unparseable DSN must return an error")
	}
	if strings.Contains(err.Error(), "secretpw") {
		t.Fatalf("parse error leaked the password: %v", err)
	}
	if strings.Contains(err.Error(), dsn) {
		t.Fatalf("parse error leaked the dsn: %v", err)
	}
}
