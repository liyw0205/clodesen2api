package admin

import (
	"net/http/httptest"
	"testing"

	"clodesen2api/internal/usage"
)

func TestRequestRecordCategory(t *testing.T) {
	tests := []struct {
		name   string
		record usage.RequestRecord
		want   string
	}{
		{name: "normal", record: usage.RequestRecord{Success: true, Status: 200}, want: "normal"},
		{name: "account upstream error", record: usage.RequestRecord{AccountID: "account-1", Status: 502}, want: "upstream_error"},
		{name: "unattributed error", record: usage.RequestRecord{Status: 400}, want: "abnormal"},
		{name: "network failure", record: usage.RequestRecord{AccountID: "", Error: "dial failed"}, want: "abnormal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := requestRecordCategory(test.record); got != test.want {
				t.Fatalf("requestRecordCategory() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFilterRequestRecordsByStatusAndAccount(t *testing.T) {
	records := []usage.RequestRecord{
		{AccountID: "id-one", Account: "one@example.test", Status: 200, Success: true},
		{AccountID: "id-one", Account: "one@example.test", Status: 502, Error: "upstream failed"},
		{Status: 400, Error: "invalid request"},
	}
	request := httptest.NewRequest("GET", "/admin/api/request-records?account=one%40example.test&status=upstream_error", nil)
	filtered := filterRequestRecords(records, request)
	if len(filtered) != 1 || filtered[0].Error != "upstream failed" {
		t.Fatalf("filtered records = %+v, want only one account's upstream error", filtered)
	}
	options := requestRecordAccounts(records)
	if len(options) != 1 || options[0].ID != "one@example.test" {
		t.Fatalf("account options = %+v, want identifiers present in records", options)
	}
}
