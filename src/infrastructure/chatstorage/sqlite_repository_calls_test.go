package chatstorage

import "testing"

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
