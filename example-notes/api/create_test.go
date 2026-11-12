package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/notes/notes"
)

func TestCreate(t *testing.T) {
	srv := httptest.NewServer(Handler(notes.NewStore()))
	defer srv.Close()

	res, err := http.Post(srv.URL+"/notes", "application/json", strings.NewReader(`{"title":" Groceries ","body":"milk"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status %d, want 201", res.StatusCode)
	}
}

func TestCreateNeedsATitle(t *testing.T) {
	srv := httptest.NewServer(Handler(notes.NewStore()))
	defer srv.Close()

	res, err := http.Post(srv.URL+"/notes", "application/json", strings.NewReader(`{"title":"  "}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", res.StatusCode)
	}
}
