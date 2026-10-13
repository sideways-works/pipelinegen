package plugins

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestABindUnitReadsAsJSONWouldReadIt(t *testing.T) {
	entries := []string{
		`[]`, `[3]`, `[3,"executed"]`, `[3, "closure", 1]`, `[3,"closure",-1]`,
		`[ 3 , "closure" , 2 , [ 1 , 2 ] ]`, `[3,"k",2,[]]`, `[0,"",0,[0]]`,
		`[3.0,"k"]`, `[3e0]`, `[3,"ké"]`, `[3,"k\"x"]`, `[3,"é"]`, `[3,null,2]`,
		`[3,"k",null,[1]]`, `[3,"k",2,"x"]`, `[3,"k",2,[1,"x",2]]`, `[3,"k",2,[1],"more"]`,
		`["x"]`, `[3,7]`, `[-0]`, `[3,"k",2.5]`,
	}

	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // test data
	for range 2000 {
		parts := []string{strconv.Itoa(r.IntN(100000))}
		if r.IntN(4) > 0 {
			parts = append(parts, `"`+[]string{"executed", "closure", "dependent", "x y", ""}[r.IntN(5)]+`"`)
		}

		if len(parts) == 2 && r.IntN(3) > 0 {
			parts = append(parts, strconv.Itoa(r.IntN(1000)-1))
		}

		if len(parts) == 3 && r.IntN(2) > 0 {
			var vs []string
			for range r.IntN(5) {
				vs = append(vs, strconv.Itoa(r.IntN(1000)))
			}

			parts = append(parts, "["+strings.Join(vs, ",")+"]")
		}

		entries = append(entries, "["+strings.Join(parts, ",")+"]")
	}

	for _, e := range entries {
		var want bindUnit
		if err := want.fromValues([]byte(e)); err != nil {
			t.Fatalf("%s: %v", e, err)
		}

		var got []bindUnit
		if err := json.Unmarshal([]byte("["+e+"]"), &got); err != nil {
			t.Fatalf("%s: %v", e, err)
		}

		if len(want.vias) == 0 {
			want.vias = nil
		}

		if len(got[0].vias) == 0 {
			got[0].vias = nil
		}

		if !reflect.DeepEqual(got[0], want) {
			t.Errorf("%s: read %+v, JSON's values give %+v", e, got[0], want)
		}
	}
}
