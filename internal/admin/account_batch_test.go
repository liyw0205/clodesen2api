package admin

import (
	"net/http"
	"net/http/cookiejar"
	"testing"

	"clodesen2api/internal/upstream"
)

func TestBatchAccountsAuthenticationValidationAndAtomicUpdate(t *testing.T) {
	server, store, up, _ := newAdminTestServer(t)
	accounts := store.Accounts()
	accounts = append(accounts, upstream.AccountCredential{ID: "account-two", Identifier: "two@example.test", Password: "second-password", Enabled: true})
	if err := store.Replace(accounts); err != nil {
		t.Fatal(err)
	}
	if err := up.ReplaceAccounts(accounts); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	url := server.URL + "/admin/api/accounts/batch"
	body := `{"accountIds":["account-one","account-two"],"enabled":false}`
	response, _ := requestJSON(t, client, http.MethodPost, url, body, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
	}
	response, _ = requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/session", jsonBody(t, map[string]string{"token": testAdminToken}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatal("login failed")
	}
	response, _ = requestJSON(t, client, http.MethodPost, url, body, "https://other.example.test")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross origin status = %d", response.StatusCode)
	}
	for _, invalid := range []string{
		`{"accountIds":[],"enabled":false}`,
		`{"accountIds":["account-one"]}`,
		`{"accountIds":["account-one","account-one"],"enabled":false}`,
		`{"accountIds":["account-one"],"enabled":false,"password":"unexpected"}`,
	} {
		response, _ = requestJSON(t, client, http.MethodPost, url, invalid, "")
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid request accepted: %s: %d", invalid, response.StatusCode)
		}
	}
	response, _ = requestJSON(t, client, http.MethodPost, url, `{"accountIds":["account-one","missing"],"enabled":false}`, "")
	if response.StatusCode != http.StatusNotFound || !store.Accounts()[0].Enabled {
		t.Fatal("missing account caused a partial update")
	}
	response, result := requestJSON(t, client, http.MethodPost, url, body, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disable failed: %d %s", response.StatusCode, result)
	}
	for _, account := range store.Accounts() {
		if account.Enabled {
			t.Fatal("account remains enabled")
		}
	}
	response, _ = requestJSON(t, client, http.MethodPost, url, `{"accountIds":["account-two"],"enabled":true}`, "")
	if response.StatusCode != http.StatusOK {
		t.Fatal("enable failed")
	}
	got := store.Accounts()
	if got[0].Enabled || !got[1].Enabled || got[0].Password != "private-password" || got[1].Password != "second-password" {
		t.Fatal("enable changed an unrelated account or credential")
	}
}
