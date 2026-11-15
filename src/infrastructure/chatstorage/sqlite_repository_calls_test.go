package chatstorage

import (
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
)

// The device registry keeps the per-device calls switch of the whatsapp package.
var _ whatsapp.DeviceCallsStore = (*SQLiteRepository)(nil)

func TestDeviceCallsEnabledRoundTrip(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	on, found, err := repo.GetDeviceCallsEnabled("slot-a")
	if err != nil || found || on {
		t.Fatalf("empty table: got (%v, %v, %v), want (false, false, nil)", on, found, err)
	}
	if err := repo.SetDeviceCallsEnabled("slot-a", true); err != nil {
		t.Fatal(err)
	}
	if on, found, err = repo.GetDeviceCallsEnabled("slot-a"); err != nil || !found || !on {
		t.Fatalf("after on: got (%v, %v, %v)", on, found, err)
	}
	if err := repo.SetDeviceCallsEnabled("slot-a", false); err != nil {
		t.Fatal(err)
	}
	if on, found, err = repo.GetDeviceCallsEnabled("slot-a"); err != nil || !found || on {
		t.Fatalf("after off: got (%v, %v, %v)", on, found, err)
	}
	if err := repo.SetDeviceCallsEnabled(" ", true); err == nil {
		t.Fatal("a blank device id must fail")
	}
}

func TestSeedDeviceCallsEnabledNeverOverwrites(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	if err := repo.SeedDeviceCallsEnabled("slot-a", true); err != nil {
		t.Fatal(err)
	}
	if on, found, _ := repo.GetDeviceCallsEnabled("slot-a"); !found || !on {
		t.Fatal("seed must store the first value")
	}
	if err := repo.SetDeviceCallsEnabled("slot-a", false); err != nil {
		t.Fatal(err)
	}
	if err := repo.SeedDeviceCallsEnabled("slot-a", true); err != nil {
		t.Fatal(err)
	}
	if on, found, _ := repo.GetDeviceCallsEnabled("slot-a"); !found || on {
		t.Fatal("seed overwrote a stored value")
	}
}

func intPtr(v int) *int { return &v }

func TestDeviceCallsMaxCallsRoundTrip(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	if n, set, err := repo.GetDeviceCallsMaxCalls("slot-a"); err != nil || set || n != 0 {
		t.Fatalf("no row: got (%d, %v, %v), want (0, false, nil)", n, set, err)
	}
	if err := repo.SetDeviceCallsConfig("slot-a", true, nil); err != nil {
		t.Fatal(err)
	}
	if n, set, err := repo.GetDeviceCallsMaxCalls("slot-a"); err != nil || set || n != 0 {
		t.Fatalf("NULL: got (%d, %v, %v), want (0, false, nil)", n, set, err)
	}
	if err := repo.SetDeviceCallsConfig("slot-a", true, intPtr(3)); err != nil {
		t.Fatal(err)
	}
	if n, set, err := repo.GetDeviceCallsMaxCalls("slot-a"); err != nil || !set || n != 3 {
		t.Fatalf("after 3: got (%d, %v, %v)", n, set, err)
	}
	// A nil max_calls keeps the stored value; enabled still changes.
	if err := repo.SetDeviceCallsConfig("slot-a", false, nil); err != nil {
		t.Fatal(err)
	}
	if n, set, _ := repo.GetDeviceCallsMaxCalls("slot-a"); !set || n != 3 {
		t.Fatalf("nil must keep 3, got (%d, %v)", n, set)
	}
	if on, found, _ := repo.GetDeviceCallsEnabled("slot-a"); !found || on {
		t.Fatalf("enabled must be false, got (%v, %v)", on, found)
	}
	// SetDeviceCallsEnabled leaves max_calls alone.
	if err := repo.SetDeviceCallsEnabled("slot-a", true); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := repo.GetDeviceCallsMaxCalls("slot-a"); n != 3 {
		t.Fatalf("SetDeviceCallsEnabled must keep 3, got %d", n)
	}
	if err := repo.SetDeviceCallsConfig(" ", true, intPtr(2)); err == nil {
		t.Fatal("a blank device id must fail")
	}
}

func TestDeviceCallsMaxCallsColumnIsAddedToAnOldTable(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	// The table as stage 2b created it, with a row.
	if _, err := repo.db.Exec(`CREATE TABLE elphant_device_calls (
		device_id VARCHAR(255) PRIMARY KEY,
		enabled BOOLEAN NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`INSERT INTO elphant_device_calls (device_id, enabled, updated_at) VALUES ('slot-old', 1, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	if n, set, err := repo.GetDeviceCallsMaxCalls("slot-old"); err != nil || set || n != 0 {
		t.Fatalf("old row reads NULL: got (%d, %v, %v)", n, set, err)
	}
	if on, found, err := repo.GetDeviceCallsEnabled("slot-old"); err != nil || !found || !on {
		t.Fatalf("old row keeps enabled: got (%v, %v, %v)", on, found, err)
	}
	if err := repo.SetDeviceCallsConfig("slot-old", true, intPtr(2)); err != nil {
		t.Fatal(err)
	}
	if n, set, _ := repo.GetDeviceCallsMaxCalls("slot-old"); !set || n != 2 {
		t.Fatalf("after 2: got (%d, %v)", n, set)
	}
	var columns int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('elphant_device_calls') WHERE name = 'max_calls'`).Scan(&columns); err != nil || columns != 1 {
		t.Fatalf("max_calls columns: got %d (%v), want 1", columns, err)
	}
}
