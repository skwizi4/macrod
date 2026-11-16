package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnerGuard(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "macro.db")
	host := "container-1"
	hostname = func() (string, error) { return host, nil }
	defer func() { hostname = os.Hostname }()

	release, err := ClaimOwner(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	// same host (macroctl exec'd into the daemon's container): allowed
	st, err := Open(ctx, db)
	if err != nil {
		t.Fatalf("same host: %v", err)
	}
	st.Close()

	// another host (the Mac): refused with advice
	host = "macbook"
	if _, err := Open(ctx, db); !errors.Is(err, ErrForeignOwner) {
		t.Fatalf("foreign host: %v", err)
	}
	if err := CheckOwner(db); !errors.Is(err, ErrForeignOwner) {
		t.Fatalf("CheckOwner: %v", err)
	}
	// a mark not refreshed for longer than ownerStale (crashed daemon): allowed
	if err := checkOwner(db, time.Now().Add(ownerStale+time.Second)); err != nil {
		t.Fatalf("stale mark: %v", err)
	}

	release()
	if _, err := os.Stat(filepath.Join(filepath.Dir(db), OwnerFile)); !os.IsNotExist(err) {
		t.Errorf("owner file left after release: %v", err)
	}
	st, err = Open(ctx, db)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	st.Close()
}
