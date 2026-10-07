package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// CallValidate asks a driver that says Validates whether steps' settings
// hold before anything runs. It is no verb of a step, so it is not among
// DriverVerbs.
const CallValidate = "validate"

// ErrSettings is a step's settings a driver refuses.
var ErrSettings = errors.New("settings")

// SettingError is one refusal of a step's settings: Place is where in
// the with block (checks[2].timout), empty for the block as a whole.
type SettingError struct {
	Place   string `json:"place,omitempty"`
	Problem string `json:"problem"`
}

func (e *SettingError) Error() string {
	if e.Place == "" {
		return "settings: " + e.Problem
	}

	return "settings: " + e.Place + ": " + e.Problem
}

// Unwrap makes every SettingError an ErrSettings.
func (e *SettingError) Unwrap() error { return ErrSettings }

// ValidateRequest is a validate call's request: steps of one driver, each
// with its verb and its settings as a run would get them.
type ValidateRequest struct {
	Request

	Steps []ValidateStep `json:"steps"`
}

// ValidateStep is one step a validate call checks.
type ValidateStep struct {
	Verb     string         `json:"verb"`
	Settings map[string]any `json:"settings"`
}

// ValidateAnswer has one result per step of the request, in its order.
type ValidateAnswer struct {
	Steps []ValidateResult `json:"steps"`
}

// ValidateResult is a step's refusals; none is a step the driver takes.
type ValidateResult struct {
	Problems []SettingError `json:"problems,omitempty"`
}

// SettingNoun is what a settings struct's keys belong to in a refusal
// ("a check"); a struct that doesn't say is "the settings".
type SettingNoun interface{ SettingNoun() string }

// DecodeSettings decodes a step's settings into v, a pointer to a struct,
// after checking them against v's type at every depth: an unknown key, a
// value of another type or a key with no value is refused with a
// SettingError naming its place. A setting a driver ignored would leave its
// step running without it, unseen.
func DecodeSettings(conf map[string]any, v any) error {
	data, err := json.Marshal(conf)
	if err != nil {
		return &SettingError{Problem: err.Error()}
	}

	// the settings as JSON gives them, whatever Go values they came as
	var plain any

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	if err := dec.Decode(&plain); err != nil {
		return &SettingError{Problem: err.Error()}
	}

	if plain == nil {
		plain = map[string]any{}
	}

	if err := walkSetting(plain, reflect.TypeOf(v).Elem(), ""); err != nil {
		return err
	}

	if err := json.Unmarshal(data, v); err != nil {
		return &SettingError{Problem: err.Error()}
	}

	return nil
}

func walkSetting(val any, t reflect.Type, place string) error {
	// a key with no value is a value left out: the default would hold,
	// unseen
	if val == nil {
		return &SettingError{Place: place, Problem: "has no value"}
	}

	if t.Kind() == reflect.Pointer {
		return walkSetting(val, t.Elem(), place)
	}

	switch t.Kind() {
	case reflect.Struct:
		m, ok := val.(map[string]any)
		if !ok {
			return mistyped(place, "a map of keys", val)
		}

		return walkStruct(m, t, place)
	case reflect.Slice:
		list, ok := val.([]any)
		if !ok {
			return mistyped(place, "a list", val)
		}

		for i, item := range list {
			if err := walkSetting(item, t.Elem(), fmt.Sprintf("%s[%d]", place, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		m, ok := val.(map[string]any)
		if !ok {
			return mistyped(place, "a map", val)
		}

		for _, k := range slices.Sorted(maps.Keys(m)) {
			if err := walkSetting(m[k], t.Elem(), fmt.Sprintf("%s[%s]", place, strconv.Quote(k))); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := val.(string); !ok {
			return mistyped(place, "a string", val)
		}
	case reflect.Bool:
		if _, ok := val.(bool); !ok {
			return mistyped(place, "true or false", val)
		}
	case reflect.Int, reflect.Int64:
		if n, ok := val.(json.Number); !ok || !isInt(n) {
			return mistyped(place, "a whole number", val)
		}
	case reflect.Interface:
		// the driver reads it as it comes
	default:
		return &SettingError{Place: place, Problem: "the driver can't read a " + t.Kind().String()}
	}

	return nil
}

func isInt(n json.Number) bool {
	_, err := n.Int64()

	return err == nil
}

func walkStruct(m map[string]any, t reflect.Type, place string) error {
	fields := map[string]reflect.Type{}

	var keys []string

	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "" || name == "-" {
			continue
		}

		fields[name] = f.Type
		keys = append(keys, name)
	}

	for _, k := range slices.Sorted(maps.Keys(m)) {
		ft, ok := fields[k]
		if !ok {
			return &SettingError{Place: joinPlace(place, k), Problem: "unknown key; the keys of " + nounOf(t) + " are " + strings.Join(keys, ", ")}
		}

		if err := walkSetting(m[k], ft, joinPlace(place, k)); err != nil {
			return err
		}
	}

	return nil
}

func nounOf(t reflect.Type) string {
	if n, ok := reflect.TypeAssert[SettingNoun](reflect.New(t)); ok {
		return n.SettingNoun()
	}

	return "the settings"
}

func mistyped(place, want string, val any) error {
	return &SettingError{Place: place, Problem: "takes " + want + ", not " + shownSetting(val)}
}

func shownSetting(val any) string {
	switch v := val.(type) {
	case string:
		return strconv.Quote(v)
	case []any:
		return "a list"
	case map[string]any:
		return "a map"
	default:
		return fmt.Sprint(v)
	}
}

func joinPlace(place, key string) string {
	if place == "" {
		return key
	}

	return place + "." + key
}

// SettingProblems are a validate result's refusals from a driver's
// error: its SettingError, or the error's message as a whole.
func SettingProblems(err error) []SettingError {
	if err == nil {
		return nil
	}

	if se, ok := errors.AsType[*SettingError](err); ok {
		return []SettingError{*se}
	}

	return []SettingError{{Problem: strings.TrimPrefix(err.Error(), "settings: ")}}
}

// Validate asks a driver that says Validates for its refusals of steps'
// settings: one result per step, in their order.
func (p Plugin) Validate(ctx context.Context, work string, steps []ValidateStep) ([]ValidateResult, error) {
	var a ValidateAnswer
	if err := p.Call(ctx, CallValidate, ValidateRequest{Request: p.NewRequest(work), Steps: steps}, &a); err != nil {
		return nil, err
	}

	if len(a.Steps) != len(steps) {
		return nil, fmt.Errorf("%w: %s validate answered %d steps of %d", ErrFailed, p.Name, len(a.Steps), len(steps))
	}

	return a.Steps, nil
}
