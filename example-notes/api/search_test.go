package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/notes/notes"
)

func TestSearch(t *testing.T) {
	s := notes.NewStore()
	s.Add("Groceries", "milk, eggs")
	s.Add("Books", "a novel")

	srv := httptest.NewServer(Handler(s))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/notes?q=milk")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	var got []notes.Note
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Title != "Groceries" {
		t.Fatalf("search milk = %v", got)
	}
}
