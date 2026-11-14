package netdisk

import (
	"path/filepath"
	"testing"
	"time"
)

func TestImportAllMergesNewerAccountsAndCookies(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "accounts.json"), filepath.Join(dir, "cookies.json"))

	old := Account{ID: "acc-1", Provider: ProviderQuark, DisplayName: "旧", UpdatedAt: time.Unix(100, 0)}
	if err := store.SaveAccounts([]Account{old}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCookie("acc-1", "old-cookie"); err != nil {
		t.Fatal(err)
	}

	newer := old
	newer.DisplayName = "新"
	newer.UpdatedAt = time.Unix(200, 0)
	added := Account{ID: "acc-2", Provider: ProviderUC, DisplayName: "新增", UpdatedAt: time.Unix(150, 0)}
	if n, err := store.ImportAll([]Account{newer, added}, map[string]string{"acc-2": "cookie-2"}); err != nil || n == 0 {
		t.Fatalf("import: n=%d err=%v", n, err)
	}

	accounts, err := store.LoadAccounts()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Account{}
	for _, a := range accounts {
		byID[a.ID] = a
	}
	if byID["acc-1"].DisplayName != "新" {
		t.Fatalf("newer account must win: %+v", byID["acc-1"])
	}
	if _, ok := byID["acc-2"]; !ok {
		t.Fatal("new account missing")
	}
	if cookie, _ := store.LoadCookie("acc-2"); cookie != "cookie-2" {
		t.Fatalf("cookie not imported: %q", cookie)
	}
	// The existing cookie stays when the import does not carry one for it.
	if cookie, _ := store.LoadCookie("acc-1"); cookie != "old-cookie" {
		t.Fatalf("existing cookie lost: %q", cookie)
	}

	// An older record must not overwrite the newer one.
	stale := old
	stale.UpdatedAt = time.Unix(50, 0)
	if _, err := store.ImportAll([]Account{stale}, nil); err != nil {
		t.Fatal(err)
	}
	accounts, _ = store.LoadAccounts()
	if accounts[0].DisplayName != "新" {
		t.Fatalf("stale import overwrote account: %+v", accounts[0])
	}
}
