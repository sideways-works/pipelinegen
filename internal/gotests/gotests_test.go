package gotests

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"
)

const orders = `package api

import "testing"

func TestMain(m *testing.M) {}

func TestOrders(t *testing.T) {}

func Test(t *testing.T) {}

func Testify() {}

func Test_lower(t *testing.T) {}

func FuzzAmount(f *testing.F) {}

func ExampleTotal() {}

func Example_refund() {}

func BenchmarkTotal(b *testing.B) {}

func helper() {}

type suite struct{}

func (suite) TestMethod(t *testing.T) {}
`

func TestNamesAreWhatGoTestRuns(t *testing.T) {
	got, err := Names("orders_test.go", []byte(orders))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"ExampleTotal", "Example_refund", "FuzzAmount", "Test", "TestOrders", "Test_lower"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestByFileAndFileOf(t *testing.T) {
	files := map[string]string{
		"api/orders_test.go": orders,
		"api/wallet_test.go": "package api\n\nimport \"testing\"\n\nfunc TestWallet(t *testing.T) {}\n",
	}

	read := func(p string) ([]byte, error) {
		s, ok := files[p]
		if !ok {
			return nil, fs.ErrNotExist
		}

		return []byte(s), nil
	}

	by, err := ByFile([]string{"api/orders_test.go", "api/wallet_test.go"}, read)
	if err != nil {
		t.Fatal(err)
	}

	of := FileOf(by)
	if of["TestWallet"] != "api/wallet_test.go" || of["FuzzAmount"] != "api/orders_test.go" || len(of) != 7 {
		t.Fatalf("file of: %v", of)
	}

	if _, err := ByFile([]string{"api/gone_test.go"}, read); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing file: %v", err)
	}

	if _, err := Names("bad_test.go", []byte("package")); err == nil {
		t.Fatal("unparsable source has names")
	}
}
