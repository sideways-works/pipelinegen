package affected

import (
	"testing"
	"time"
)

func TestOnePlanAtATimePerCache(t *testing.T) {
	cache := t.TempDir()

	unlock, err := lockCache(cache)
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan func())

	go func() {
		second, err := lockCache(cache)
		if err != nil {
			t.Error(err)

			second = func() {}
		}

		got <- second
	}()

	select {
	case second := <-got:
		second()
		t.Fatal("a second plan got the cache while the first held it")
	case <-time.After(200 * time.Millisecond):
	}

	unlock()

	select {
	case second := <-got:
		second()
	case <-time.After(5 * time.Second):
		t.Fatal("the second plan never got the cache")
	}
}
